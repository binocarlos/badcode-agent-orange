package orchestrator

import (
	"context"
	"fmt"
	"net/http"
	"os"
)

// The ModelRouter seam is declared in contracts.go (§5). This file provides the
// map-backed impl plus config wiring for a fleet of real Anthropic models.

// TierRouter maps each ModelTier to one Model. An unmapped tier resolves to a
// loud errModel — For cannot return an error, so a mis-tier fails on Run rather
// than panicking on a nil Model.
type TierRouter struct {
	models map[ModelTier]Model
}

// NewTierRouter builds a router over the given tier->Model map.
func NewTierRouter(models map[ModelTier]Model) *TierRouter {
	return &TierRouter{models: models}
}

func (r *TierRouter) For(tier ModelTier) Model {
	if m, ok := r.models[tier]; ok {
		return m
	}
	return errModel{tier: tier}
}

// errModel is the fail-loud placeholder for an unmapped tier.
type errModel struct{ tier ModelTier }

func (e errModel) Run(context.Context, string) (string, Usage, error) {
	return "", Usage{}, fmt.Errorf("no model configured for tier %q", e.tier)
}

var (
	_ ModelRouter = (*TierRouter)(nil)
	_ Model       = errModel{}
)

// DefaultModelIDs returns the current default model ID per tier. These are the
// confirmed IDs for this environment (Slice B reconciliation brief). Model IDs
// drift; they are env-overridable via ModelIDsFromEnv.
// Current defaults: full=Opus, mid=Sonnet, cheap=Haiku.
func DefaultModelIDs() map[ModelTier]string {
	return map[ModelTier]string{
		TierFull:  "claude-opus-4-8",
		TierMid:   "claude-sonnet-5",
		TierCheap: "claude-haiku-4-5",
	}
}

// ModelIDsFromEnv starts from DefaultModelIDs and lets each tier be overridden by
// AGENTKIT_MODEL_FULL / _MID / _CHEAP (config-driven, never hardcoded at a call site).
func ModelIDsFromEnv() map[ModelTier]string {
	ids := DefaultModelIDs()
	if v := os.Getenv("AGENTKIT_MODEL_FULL"); v != "" {
		ids[TierFull] = v
	}
	if v := os.Getenv("AGENTKIT_MODEL_MID"); v != "" {
		ids[TierMid] = v
	}
	if v := os.Getenv("AGENTKIT_MODEL_CHEAP"); v != "" {
		ids[TierCheap] = v
	}
	return ids
}

// DefaultPricing returns current per-tier pricing (USD per million tokens). These
// are documented, env-overridable estimates that drive the ceiling estimate, not
// a safety gate — pricing drifts.
func DefaultPricing() map[ModelTier]Pricing {
	return map[ModelTier]Pricing{
		TierFull:  {InputPerMTok: 5, OutputPerMTok: 25},
		TierMid:   {InputPerMTok: 3, OutputPerMTok: 15},
		TierCheap: {InputPerMTok: 1, OutputPerMTok: 5},
	}
}

// RouterConfig configures a router of real Anthropic models. ModelIDs/Pricing
// default to the env/documented values when nil; Meter (optional) is shared
// across every tier so the monthly ceiling spans the whole fleet.
type RouterConfig struct {
	APIKey     string
	ModelIDs   map[ModelTier]string
	Pricing    map[ModelTier]Pricing
	Meter      SpendMeter
	BaseURL    string
	HTTPClient *http.Client
	MaxTokens  int
}

// NewAnthropicRouter builds one AnthropicModel per configured tier.
func NewAnthropicRouter(cfg RouterConfig) *TierRouter {
	ids := cfg.ModelIDs
	if ids == nil {
		ids = ModelIDsFromEnv()
	}
	pricing := cfg.Pricing
	if pricing == nil {
		pricing = DefaultPricing()
	}
	models := make(map[ModelTier]Model, len(ids))
	for tier, id := range ids {
		models[tier] = &AnthropicModel{
			APIKey:     cfg.APIKey,
			ModelID:    id,
			BaseURL:    cfg.BaseURL,
			HTTPClient: cfg.HTTPClient,
			Meter:      cfg.Meter,
			Pricing:    pricing[tier],
			MaxTokens:  cfg.MaxTokens,
		}
	}
	return NewTierRouter(models)
}
