package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OpenAI is a Provider backed by OpenAI's /v1/embeddings endpoint.
//
// # Why this one is in-tree when the doc says "real hosted providers are host code"
//
// Because "host code" left every real deployment with a dead semantic leg: the
// only shipped providers were `none` and `mock`, so hybrid search was keyword-
// only everywhere and the RRF fusion path was exercised by tests alone. One
// working provider in the box is the difference between a designed feature and
// a used one. The seam is unchanged — this is an implementation of Provider
// like any other, and a host is still free to pass its own.
//
// # Dimensions
//
// `Dim` is 1536, fixed by migration 022's `vector(1536)` column.
// `text-embedding-3-small` emits exactly that natively, which is why it is the
// default. This client ALSO sends the `dimensions` parameter explicitly, so
// `text-embedding-3-large` (natively 3072) is truncated by OpenAI to 1536 and
// remains usable. That is a supported reduction — the -3 models are trained so
// prefixes of the vector stay meaningful — not a client-side truncation.
//
// # The thing to know before switching models later
//
// Memories are append-only and are embedded exactly once, at create. Vectors
// from two different models are NOT comparable, so changing `model` after rows
// exist silently degrades ranking: old rows are scored against a new query
// space. There is no re-embed job. Choose once, or plan a backfill.
type OpenAI struct {
	apiKey  string
	model   string
	baseURL string
	client  *http.Client
}

// compile-time interface check
var _ Provider = (*OpenAI)(nil)

// OpenAIConfig configures NewOpenAI. Only APIKey is required.
type OpenAIConfig struct {
	APIKey string
	// Model defaults to text-embedding-3-small (1536 dims natively, cheapest
	// of the -3 family, and the only one needing no reduction).
	Model string
	// BaseURL defaults to https://api.openai.com/v1. Override for an
	// OpenAI-compatible endpoint (Azure, a local server, a gateway).
	BaseURL string
	// HTTPClient defaults to a client with a 30s timeout. memory_create blocks
	// on this call, so an unbounded one would hang a session's tool call.
	HTTPClient *http.Client
}

const (
	DefaultOpenAIModel   = "text-embedding-3-small"
	DefaultOpenAIBaseURL = "https://api.openai.com/v1"
)

// NewOpenAI builds the provider. It does not call the API — a bad key surfaces
// on first use, not at boot, because failing the whole process over the
// embedding endpoint would take down chat as well.
func NewOpenAI(cfg OpenAIConfig) (*OpenAI, error) {
	key := strings.TrimSpace(cfg.APIKey)
	if key == "" {
		return nil, fmt.Errorf("embedding: OpenAI provider needs an API key (set OPENAI_API_KEY)")
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = DefaultOpenAIModel
	}
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if base == "" {
		base = DefaultOpenAIBaseURL
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &OpenAI{apiKey: key, model: model, baseURL: base, client: client}, nil
}

// Model reports the model in use, for the boot log.
func (o *OpenAI) Model() string { return o.model }

type openAIRequest struct {
	Input      string `json:"input"`
	Model      string `json:"model"`
	Dimensions int    `json:"dimensions"`
}

type openAIResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// Embed implements Provider. Safe for concurrent use: http.Client is, and this
// type is immutable after construction.
func (o *OpenAI) Embed(ctx context.Context, text string) ([]float32, error) {
	if strings.TrimSpace(text) == "" {
		return nil, ErrEmptyText
	}

	body, err := json.Marshal(openAIRequest{Input: text, Model: o.model, Dimensions: Dim})
	if err != nil {
		return nil, fmt.Errorf("embedding: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("embedding: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+o.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedding: openai request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Cap the read: a proxy returning an HTML error page should not be buffered
	// without limit into an agent's tool call.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("embedding: read openai response: %w", err)
	}

	var parsed openAIResponse
	if jsonErr := json.Unmarshal(raw, &parsed); jsonErr != nil {
		// Non-JSON body: report the status and a snippet rather than a parse
		// error, which would hide a 401 or a gateway page behind "invalid
		// character '<'".
		return nil, fmt.Errorf("embedding: openai returned %s with an unparseable body: %s",
			resp.Status, snippet(raw))
	}
	if parsed.Error != nil {
		return nil, fmt.Errorf("embedding: openai %s: %s", resp.Status, parsed.Error.Message)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embedding: openai returned %s: %s", resp.Status, snippet(raw))
	}
	if len(parsed.Data) == 0 {
		return nil, fmt.Errorf("embedding: openai returned no embedding for a non-empty input")
	}

	// Validate here as well as in Embed(): a directly-constructed provider
	// (a host wiring its own) must fail loudly rather than hand agentdb a
	// wrong-width vector that the INSERT would reject far from the cause.
	v := parsed.Data[0].Embedding
	if err := Validate(v); err != nil {
		return nil, fmt.Errorf("%w (model %q — a model whose width is not %d needs the dimensions "+
			"parameter honoured, or migration 022 changed)", err, o.model, Dim)
	}
	return v, nil
}

// snippet trims a response body to something loggable.
func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		return s[:200] + "…"
	}
	if s == "" {
		return "(empty body)"
	}
	return s
}
