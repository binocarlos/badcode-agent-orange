package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const defaultAnthropicBaseURL = "https://api.anthropic.com"

// Pricing is the per-million-token cost of a model, used to convert token usage
// into the USD amount charged to the SpendMeter.
type Pricing struct {
	InputPerMTok  float64
	OutputPerMTok float64
}

func (p Pricing) usd(inTok, outTok int64) float64 {
	return (float64(inTok)*p.InputPerMTok + float64(outTok)*p.OutputPerMTok) / 1_000_000
}

// AnthropicModel is the real Model (contracts §5): prompt in, text out, backed by
// the Anthropic Messages API over HTTP with API-key auth (subscription OAuth is
// disallowed for automation). A thin stdlib client — no SDK dependency. When Meter
// is set, Run halts before dispatch if the spend ceiling is hit and records actual
// token spend after each call (metering lives here because Model.Run surfaces only
// a string, so a generic decorator cannot see usage).
type AnthropicModel struct {
	APIKey     string
	ModelID    string
	MaxTokens  int
	BaseURL    string // default https://api.anthropic.com
	APIVersion string // default 2023-06-01
	HTTPClient *http.Client
	Meter      SpendMeter // optional; halts dispatch at the ceiling
	Pricing    Pricing    // used only when Meter is set
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicReq struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	Messages  []anthropicMessage `json:"messages"`
}

type anthropicRespBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicResp struct {
	Content    []anthropicRespBlock `json:"content"`
	StopReason string               `json:"stop_reason"`
	Usage      struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
	} `json:"usage"`
}

func (m *AnthropicModel) baseURL() string {
	if m.BaseURL != "" {
		return m.BaseURL
	}
	return defaultAnthropicBaseURL
}

func (m *AnthropicModel) apiVersion() string {
	if m.APIVersion != "" {
		return m.APIVersion
	}
	return "2023-06-01"
}

func (m *AnthropicModel) maxTokens() int {
	if m.MaxTokens > 0 {
		return m.MaxTokens
	}
	return 1024
}

func (m *AnthropicModel) httpClient() *http.Client {
	if m.HTTPClient != nil {
		return m.HTTPClient
	}
	return http.DefaultClient
}

// Run sends the prompt as a single user turn and returns the concatenated text.
func (m *AnthropicModel) Run(ctx context.Context, prompt string) (string, error) {
	// Cost floor: halt before dispatch if the ceiling is already hit (§7.2).
	if m.Meter != nil {
		if err := m.Meter.Charge(ctx, 0, 0); err != nil {
			return "", fmt.Errorf("anthropic %s: %w", m.ModelID, err)
		}
	}

	body, err := json.Marshal(anthropicReq{
		Model:     m.ModelID,
		MaxTokens: m.maxTokens(),
		Messages:  []anthropicMessage{{Role: "user", Content: prompt}},
	})
	if err != nil {
		return "", fmt.Errorf("anthropic %s: marshal: %w", m.ModelID, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.baseURL()+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("anthropic %s: request: %w", m.ModelID, err)
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-api-key", m.APIKey)
	req.Header.Set("anthropic-version", m.apiVersion())

	resp, err := m.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("anthropic %s: do: %w", m.ModelID, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("anthropic %s: read: %w", m.ModelID, err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("anthropic %s: status %d: %s", m.ModelID, resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var parsed anthropicResp
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("anthropic %s: decode: %w", m.ModelID, err)
	}
	if parsed.StopReason == "refusal" {
		return "", fmt.Errorf("anthropic %s: request refused", m.ModelID)
	}

	var sb strings.Builder
	for _, b := range parsed.Content {
		if b.Type == "text" {
			sb.WriteString(b.Text)
		}
	}

	if m.Meter != nil {
		// Record actual spend. An ErrSpendCeiling here means this call pushed us
		// over — the response is still valid; the next dispatch's probe halts.
		_ = m.Meter.Charge(ctx, parsed.Usage.InputTokens+parsed.Usage.OutputTokens,
			m.Pricing.usd(parsed.Usage.InputTokens, parsed.Usage.OutputTokens))
	}
	return sb.String(), nil
}

var _ Model = (*AnthropicModel)(nil)
