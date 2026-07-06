package proxy

import (
	"math"
	"time"

	"github.com/regolo-ai/brick-SR1/apps/router/src/spatial-router/pkg/brickrouting"
	"github.com/regolo-ai/brick-SR1/apps/router/src/spatial-router/pkg/config"
	"github.com/regolo-ai/brick-SR1/apps/router/src/spatial-router/pkg/observability/logging"
	"github.com/regolo-ai/brick-SR1/apps/router/src/spatial-router/pkg/sticky"
)

// applyStickyRouting is the RoutingModeSticky decision layer. Given the router's
// candidate model, it looks up the conversation's previous model and, if
// switching away from it would cost prompt-cache reprocessing that the quality
// gain does not justify, holds on the previous model instead.
//
// It returns:
//   - stickyKey: the conversation key to record the served model against after
//     the response (empty when the conversation could not be identified, in
//     which case no post-response bookkeeping happens).
//   - model:     the model to actually use (candidate, or the held-onto previous).
//   - under:     under-capacity residual recomputed for the final model, so the
//     downstream autonomous-effort bump reflects the model actually used.
//   - switchDelta: the prefix-reprocessing cost (per-1M-token price units) that a
//     hold avoided this turn, surfaced for the routing event log. 0 on any early
//     return (no identity, no pricing, first turn) where no comparison was made.
//
// This lives entirely in the handler layer and never touches pkg/brickrouting:
// the calibrated scoring core is unchanged, only its output is post-processed.
func (s *Server) applyStickyRouting(
	apCfg *config.AnthropicPassthroughConfig,
	body []byte,
	route *brickrouting.Result,
	candidateModel string,
	candidateUnder float64,
) (stickyKey, model string, under, switchDelta float64) {
	model, under = candidateModel, candidateUnder
	if s.stickyStore == nil || route == nil {
		return "", model, under, 0
	}

	system, firstUser := extractAnthropicIdentityParts(body)
	if system == "" && firstUser == "" {
		// No stable prefix to key on: cannot participate in sticky routing.
		logging.Debugf("sticky: identity_miss reason=empty_prompt candidate=%s", candidateModel)
		return "", model, under, 0
	}
	key := sticky.HashIdentity(system, firstUser)

	prev, ok := s.stickyStore.GetAt(key, time.Now())
	if !ok {
		// First turn, or the client rewrote the stable prefix (e.g. its own
		// summarization) so the prior entry is unreachable. Either way the
		// candidate stands this turn; we still record it afterwards.
		logging.Debugf("sticky: no_prev key=%s candidate=%s", key, candidateModel)
		return key, model, under, 0
	}

	if s.pricingTable == nil {
		logging.Warnf("sticky: pricing unavailable, hysteresis skipped; candidate=%s", candidateModel)
		return key, model, under, 0
	}
	prevPrice, okPrev := s.pricingTable.Price(prev.LastModel)
	candPrice, okCand := s.pricingTable.Price(candidateModel)
	if !okPrev || !okCand {
		logging.Warnf("sticky: missing price prev=%s(ok=%v) cand=%s(ok=%v), hysteresis skipped",
			prev.LastModel, okPrev, candidateModel, okCand)
		return key, model, under, 0
	}

	in := sticky.SwitchInputs{
		PrevModel:         prev.LastModel,
		CandidateModel:    candidateModel,
		PrevScore:         scoreForModel(route, prev.LastModel),
		CandidateScore:    scoreForModel(route, candidateModel),
		PrevContextTokens: prev.LastContextTokens,
		PrevInputPrice:    prevPrice.InputPrice,
		CandInputPrice:    candPrice.InputPrice,
		CacheWriteMult:    cacheWriteInputPriceMultiplier,
		CacheReadMult:     cacheReadInputPriceMultiplier,
		ScoreMargin:       apCfg.EffectiveStickyScoreMargin(),
	}
	final, switched, reason := sticky.DecideModel(in)
	// SwitchDeltaUSD is the prefix-reprocessing cost a hold avoids (per-1M-token
	// price units, same scale as pricing.yaml). Positive on a held costed switch.
	switchDelta = sticky.SwitchDeltaUSD(in)

	if final != model {
		model = final
		under = underCapacityForModel(route, final)
	}

	logging.Infof("sticky: reason=%s prev=%s candidate=%s final=%s switched=%t prev_ctx_tokens=%d est_switch_delta_price_units=%.4f margin=%.3f",
		reason, prev.LastModel, candidateModel, final, switched, prev.LastContextTokens, switchDelta, in.ScoreMargin)

	return key, model, under, switchDelta
}

// anthropicSessionKey derives the stable conversation identity (system prompt +
// first user turn) used to group turns of the same dev session in the routing
// event log. Returns "" when there is no stable prefix to key on. Unlike
// applyStickyRouting this is mode-independent: it is computed for every routed
// request so off/sticky/orchestrator events share one session-counting basis.
func anthropicSessionKey(body []byte) string {
	system, firstUser := extractAnthropicIdentityParts(body)
	if system == "" && firstUser == "" {
		return ""
	}
	return sticky.HashIdentity(system, firstUser)
}

// scoreForModel returns the routing score (J_m, lower is better) of the named
// model from the route's score list, or NaN when the model is not in the scored
// pool (e.g. deactivated). NaN signals "no comparable score" to DecideModel.
func scoreForModel(route *brickrouting.Result, model string) float64 {
	for _, sc := range route.Scores {
		if sc.Model == model {
			return sc.Score
		}
	}
	return math.NaN()
}
