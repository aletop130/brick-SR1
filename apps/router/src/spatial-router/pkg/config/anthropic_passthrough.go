package config

import "strings"

// AnthropicPassthroughConfig configures the /v1/messages pass-through endpoint.
// When enabled, Brick exposes a transparent Anthropic-native proxy that classifies
// prompt difficulty and rewrites only the `model` field before forwarding upstream
// (typically api.anthropic.com). All other request fields and headers (Authorization,
// anthropic-version, etc.) are forwarded unchanged so the client's own credentials
// and SDK contract are preserved.
type AnthropicPassthroughConfig struct {
	Enabled     bool                         `yaml:"enabled"`
	UpstreamURL string                       `yaml:"upstream_url,omitempty"`
	ModelMap    AnthropicPassthroughModelMap `yaml:"model_map,omitempty"`

	// UseSkillRouter, when true, selects the backend model via the full Brick
	// skill router (capability classifier + skill vectors + argmin J_m over
	// skill_router.models) instead of the static complexity->ModelMap. The
	// ModelMap remains the fallback when the skill router is disabled, errors,
	// or the prompt text is empty. See pkg/proxy/anthropic.go.
	UseSkillRouter bool `yaml:"use_skill_router,omitempty"`

	// RouteSubagents, when true, sends requests that arrive with an explicit
	// native Claude model (haiku/sonnet/opus, typical of Claude Code subagents)
	// through the skill router instead of forwarding them verbatim. The Brick
	// router then picks the model by capability + complexity, the same way it
	// does for "brick-claude" traffic. Default false preserves the historic
	// passthrough behavior (native models bypass routing). See pkg/proxy/anthropic.go.
	RouteSubagents bool `yaml:"route_subagents,omitempty"`

	// UseModelRouting controls whether brick-routed traffic picks the backend
	// model dynamically (skill router / model_map) or always uses FixedModel.
	// It is a pointer so an absent key defaults to true (historic behavior:
	// model routing on). When explicitly false, every brick-routed request is
	// sent to FixedModel regardless of complexity. See ModelRoutingEnabled and
	// pkg/proxy/anthropic.go.
	UseModelRouting *bool `yaml:"use_model_routing,omitempty"`

	// UseThinkingRouting controls whether Brick injects an autonomously-computed
	// reasoning effort into brick-routed requests. It is a pointer so an absent
	// key defaults to true (historic behavior: thinking routing on). When
	// explicitly false, the client's own effort (output_config.effort) is
	// forwarded unchanged and Brick never overrides it. See
	// ThinkingRoutingEnabled and pkg/proxy/anthropic.go.
	UseThinkingRouting *bool `yaml:"use_thinking_routing,omitempty"`

	// FixedModel is the Anthropic model ID used for every brick-routed request
	// when UseModelRouting is false (e.g. "claude-opus-4-8"). Empty falls back
	// to Sonnet. Ignored when UseModelRouting is true/absent.
	FixedModel string `yaml:"fixed_model,omitempty"`

	// ExtraUsageEnabled signals that the upstream account has the paid
	// "extra-usage" tier required to use the 1M-token context window. When
	// false (default), Brick strips any "context-1m-*" anthropic-beta flag
	// from incoming requests so the call falls back to the standard 200K
	// context window — preventing the upstream "Extra usage is required for
	// 1M context" error. When true, Brick preserves the 1M flag when the
	// request body exceeds Context1MThresholdBytes and uses the
	// ModelMap1M mapping so the chosen model actually supports 1M context
	// (Haiku does not).
	ExtraUsageEnabled bool `yaml:"extra_usage_enabled,omitempty"`

	// Context1MThresholdBytes is the request body size above which the 1M
	// context window is considered necessary. Only consulted when
	// ExtraUsageEnabled is true. Default 600000 (~150K tokens) when zero.
	Context1MThresholdBytes int `yaml:"context_1m_threshold_bytes,omitempty"`

	// ModelMap1M maps complexity → model when the 1M context window is in
	// use. Haiku has no 1M variant so easy should map to Sonnet 1M.
	ModelMap1M AnthropicPassthroughModelMap `yaml:"model_map_1m,omitempty"`

	// ContextWindow, when enabled, feeds the complexity/skill classifier the
	// last K conversation turns (user + assistant) instead of only the latest
	// user message, so routing reflects accumulated context rather than a
	// single short follow-up. Default disabled (opt-in). See pkg/proxy/anthropic.go.
	ContextWindow ContextWindowConfig `yaml:"context_window,omitempty"`

	// RoutingMode selects the cache-aware routing strategy layered on top of
	// model selection. "" / "off" (default) is the historic behavior: the skill
	// router picks the model per request with no cross-turn memory. "sticky"
	// enables cache-aware hysteresis — stay on the conversation's previous model
	// unless switching is worth the prompt-cache invalidation cost. "orchestrator"
	// is the shadow-mode v2 path (computed, not served). See EffectiveRoutingMode
	// and pkg/proxy/anthropic.go.
	RoutingMode string `yaml:"routing_mode,omitempty"`

	// StickyTTLSeconds is the lifetime of a conversation's sticky routing entry.
	// Defaults to 360 (6 min) when zero — just over the 5-minute Anthropic prompt
	// cache TTL, so an expired entry always corresponds to a cold cache (switching
	// is then free and routing is unconstrained). Configurable because some tiers
	// use a 1h cache TTL. See EffectiveStickyTTLSeconds.
	StickyTTLSeconds int `yaml:"sticky_ttl_seconds,omitempty"`

	// StickyScoreMargin is how much better (lower) a candidate model's routing
	// score must be than the conversation's current model before Brick pays the
	// prompt-cache invalidation cost to switch. Defaults to 0.15 when zero. See
	// EffectiveStickyScoreMargin.
	StickyScoreMargin float64 `yaml:"sticky_score_margin,omitempty"`
}

// Routing mode identifiers for AnthropicPassthroughConfig.RoutingMode.
const (
	RoutingModeOff          = "off"
	RoutingModeSticky       = "sticky"
	RoutingModeOrchestrator = "orchestrator"
)

// ContextWindowConfig controls the context-aware classification input.
type ContextWindowConfig struct {
	Enabled bool `yaml:"enabled,omitempty"`
	// K is the number of trailing conversation turns fed to the classifier.
	// Defaults to 8 when zero (see EffectiveContextWindowK).
	K int `yaml:"k,omitempty"`
}

// EffectiveContextWindowK returns the configured K or the default of 8.
func (c *AnthropicPassthroughConfig) EffectiveContextWindowK() int {
	if c.ContextWindow.K > 0 {
		return c.ContextWindow.K
	}
	return 8
}

// AnthropicPassthroughModelMap maps complexity labels to Anthropic model IDs.
type AnthropicPassthroughModelMap struct {
	Easy   string `yaml:"easy,omitempty"`
	Medium string `yaml:"medium,omitempty"`
	Hard   string `yaml:"hard,omitempty"`
}

// EffectiveUpstreamURL returns UpstreamURL or the Anthropic public default.
func (c *AnthropicPassthroughConfig) EffectiveUpstreamURL() string {
	if c.UpstreamURL != "" {
		return strings.TrimRight(c.UpstreamURL, "/")
	}
	return "https://api.anthropic.com"
}

// Resolve maps a complexity label (e.g. "complexity:hard" or bare "hard") to a
// Anthropic model ID. Falls back to medium model for unknown labels.
func (c *AnthropicPassthroughConfig) Resolve(label string) string {
	return resolveFromMap(label, c.ModelMap, "claude-haiku-4-5", "claude-sonnet-4-6", "claude-opus-4-8")
}

// Resolve1M maps a complexity label to a model that supports the 1M-token
// context window. Defaults upgrade easy → Sonnet 1M (Haiku has no 1M variant).
func (c *AnthropicPassthroughConfig) Resolve1M(label string) string {
	return resolveFromMap(label, c.ModelMap1M, "claude-sonnet-4-6", "claude-sonnet-4-6", "claude-opus-4-8")
}

// EffectiveContext1MThresholdBytes returns the configured threshold or 600000.
func (c *AnthropicPassthroughConfig) EffectiveContext1MThresholdBytes() int {
	if c.Context1MThresholdBytes > 0 {
		return c.Context1MThresholdBytes
	}
	return 600000
}

// ModelRoutingEnabled reports whether dynamic model selection is on. An absent
// (nil) UseModelRouting key defaults to true, preserving the historic behavior
// where Brick always routes the model.
func (c *AnthropicPassthroughConfig) ModelRoutingEnabled() bool {
	return c.UseModelRouting == nil || *c.UseModelRouting
}

// ThinkingRoutingEnabled reports whether autonomous reasoning-effort injection
// is on. An absent (nil) UseThinkingRouting key defaults to true, preserving the
// historic behavior where Brick computes and injects the effort.
func (c *AnthropicPassthroughConfig) ThinkingRoutingEnabled() bool {
	return c.UseThinkingRouting == nil || *c.UseThinkingRouting
}

// EffectiveFixedModel returns the configured FixedModel or the Sonnet fallback
// when empty. Only meaningful when ModelRoutingEnabled() is false.
func (c *AnthropicPassthroughConfig) EffectiveFixedModel() string {
	if c.FixedModel != "" {
		return c.FixedModel
	}
	return "claude-sonnet-4-6"
}

// EffectiveRoutingMode returns a validated routing mode, defaulting to
// RoutingModeOff for an absent or unrecognized value so the historic per-request
// routing (no cross-turn memory) is always the safe fallback.
func (c *AnthropicPassthroughConfig) EffectiveRoutingMode() string {
	switch c.RoutingMode {
	case RoutingModeSticky, RoutingModeOrchestrator:
		return c.RoutingMode
	default:
		return RoutingModeOff
	}
}

// EffectiveStickyTTLSeconds returns the configured sticky-entry TTL or the
// default of 360 seconds (6 minutes).
func (c *AnthropicPassthroughConfig) EffectiveStickyTTLSeconds() int {
	if c.StickyTTLSeconds > 0 {
		return c.StickyTTLSeconds
	}
	return 360
}

// EffectiveStickyScoreMargin returns the configured switch margin or the
// default of 0.15.
func (c *AnthropicPassthroughConfig) EffectiveStickyScoreMargin() float64 {
	if c.StickyScoreMargin > 0 {
		return c.StickyScoreMargin
	}
	return 0.15
}

func resolveFromMap(label string, m AnthropicPassthroughModelMap, easyDefault, mediumDefault, hardDefault string) string {
	bare := strings.TrimPrefix(label, "complexity:")
	switch bare {
	case "easy":
		if m.Easy != "" {
			return m.Easy
		}
		return easyDefault
	case "hard":
		if m.Hard != "" {
			return m.Hard
		}
		return hardDefault
	default:
		if m.Medium != "" {
			return m.Medium
		}
		return mediumDefault
	}
}
