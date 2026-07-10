package proxy

import (
	"time"

	"github.com/regolo-ai/brick-SR1/apps/router/src/spatial-router/pkg/compaction"
	"github.com/regolo-ai/brick-SR1/apps/router/src/spatial-router/pkg/config"
	"github.com/regolo-ai/brick-SR1/apps/router/src/spatial-router/pkg/observability/logging"
)

// applySmartSqueeze is the RoutingModeSmartSqueeze compaction layer. It runs
// AFTER the sticky decision has picked selectedModel (smartsqueeze inherits the
// same cache-aware hysteresis), and decides whether to shrink the forwarded
// context this turn.
//
// The rule: compact ONLY when this turn switches away from the conversation's
// previous model (the new provider's prompt cache is cold, so the full prefix
// would be reprocessed at full price anyway — nothing to lose), OR when the
// conversation is already flagged compacted (keep clearing deterministically so
// the new model's cache stays warm on the reduced prefix). On a hold /
// same-model / first turn, the body is returned unchanged so the warm cache is
// preserved. system + first user turn are never touched, so the sticky identity
// key stays stable (see pkg/compaction).
//
// In shadow sub-mode (CompactShadow) the saving is measured onto ev but the raw
// body is returned; only CompactServed actually forwards the compacted body.
//
// Returns the body to forward (compacted or original) and whether it was served
// compacted (recorded into the sticky store so the next turn can continue).
func (s *Server) applySmartSqueeze(
	apCfg *config.AnthropicPassthroughConfig,
	body []byte,
	stickyKey, selectedModel string,
	ev *routingEvent,
) (out []byte, compacted bool) {
	if stickyKey == "" || s.stickyStore == nil {
		return body, false
	}
	prev, ok := s.stickyStore.GetAt(stickyKey, time.Now())
	if !ok {
		return body, false
	}
	switched := prev.LastModel != "" && prev.LastModel != selectedModel
	if !switched && !prev.Compacted {
		return body, false
	}

	// Tier-1 compaction is CPU-only (cost ~0), so any positive token saving
	// clears the economic gate; the price-weighted figure is derivable offline
	// from EstSavedTokens plus the pricing table.
	newBody, estSaved, changed, err := compaction.Compact(body, apCfg.EffectiveCompactKeepRecentTurns())
	if err != nil || !changed || estSaved <= 0 {
		return body, false
	}

	if ev != nil {
		ev.EstSavedTokens = estSaved
	}
	if apCfg.CompactServed() {
		out, compacted = newBody, true
	} else {
		out = body
		if ev != nil {
			ev.ShadowNote = "smartsqueeze_shadow"
		}
	}
	logging.Infof("smartsqueeze: prev=%s served=%s switched=%t est_saved_tokens=%d served_compacted=%t keep_recent=%d",
		prev.LastModel, selectedModel, switched, estSaved, compacted, apCfg.EffectiveCompactKeepRecentTurns())
	return out, compacted
}
