package sticky

import "math"

// Reason codes explaining a routing decision, emitted to logs/observability.
const (
	ReasonNoPrev          = "no_prev"          // no prior turn for this conversation
	ReasonSameModel       = "same_model"       // router already picked the current model
	ReasonCheapSwitch     = "cheap_switch"     // switching costs no more on the prefix (downswitch)
	ReasonQualityOverride = "quality_override" // candidate beats current by more than the margin
	ReasonPrevUnavailable = "prev_unavailable" // current model is no longer routable
	ReasonStickyHold      = "sticky_hold"      // held on current model to preserve the cache
)

// SwitchInputs carries everything DecideModel needs, all resolved by the caller
// (the handler) from route.Scores and the pricing table. Prices are input price
// per token in whatever unit the caller uses; only their ratio matters here, so
// per-1M or per-token both work as long as both prices share the unit.
type SwitchInputs struct {
	PrevModel      string
	CandidateModel string

	// PrevScore is the routing score (J_m, lower is better) of PrevModel in the
	// current pool. Set to NaN when PrevModel is no longer in the active pool.
	PrevScore float64
	// CandidateScore is the routing score of the argmin candidate.
	CandidateScore float64

	// PrevContextTokens is the cached prefix size that a switch would force the
	// new model to reprocess at cache-write price.
	PrevContextTokens int64

	PrevInputPrice float64 // input price of PrevModel
	CandInputPrice float64 // input price of CandidateModel

	CacheWriteMult float64 // e.g. 1.25 (5-min cache write)
	CacheReadMult  float64 // e.g. 0.10 (cache read)
	ScoreMargin    float64 // required score improvement to justify a costed switch
}

// SwitchDeltaUSD is the extra cost, on the cached prefix only, of switching to
// the candidate this turn versus staying on the current model:
//
//	staying  = prefix * cacheReadMult  * prevInputPrice   (prefix served from cache)
//	switching = prefix * cacheWriteMult * candInputPrice   (prefix re-written to the new model's cache)
//
// A value <= 0 means the switch is no more expensive on the prefix (typical for
// a downswitch to a much cheaper model), so hysteresis does not apply.
func SwitchDeltaUSD(in SwitchInputs) float64 {
	return float64(in.PrevContextTokens) *
		(in.CacheWriteMult*in.CandInputPrice - in.CacheReadMult*in.PrevInputPrice)
}

// DecideModel applies cache-aware hysteresis. It returns the model to actually
// use, whether that is a switch away from PrevModel, and a reason code.
//
// Rules, in order:
//  1. No prior model -> use the candidate.
//  2. Candidate == current -> keep (no switch to reason about).
//  3. Switch delta <= 0 -> switch freely (downswitch is not more expensive).
//  4. Switch delta > 0 -> switch only if the candidate's score beats the
//     current model's by more than ScoreMargin; if the current model is no
//     longer routable (PrevScore is NaN), switch. Otherwise hold.
func DecideModel(in SwitchInputs) (model string, switched bool, reason string) {
	if in.PrevModel == "" {
		return in.CandidateModel, in.CandidateModel != "", ReasonNoPrev
	}
	if in.CandidateModel == in.PrevModel {
		return in.PrevModel, false, ReasonSameModel
	}
	if SwitchDeltaUSD(in) <= 0 {
		return in.CandidateModel, true, ReasonCheapSwitch
	}
	if math.IsNaN(in.PrevScore) {
		return in.CandidateModel, true, ReasonPrevUnavailable
	}
	if in.PrevScore-in.CandidateScore > in.ScoreMargin {
		return in.CandidateModel, true, ReasonQualityOverride
	}
	return in.PrevModel, false, ReasonStickyHold
}
