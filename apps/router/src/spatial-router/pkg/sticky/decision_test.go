package sticky

import (
	"math"
	"testing"
)

// Prices are illustrative per-1M input prices (USD). Ratios are what matter.
const (
	priceHaiku = 1.0  // claude-haiku-4-5
	priceSonn  = 3.0  // claude-sonnet
	priceOpus  = 5.0  // claude-opus
	cw         = 1.25 // cache write multiplier
	cr         = 0.10 // cache read multiplier
	margin     = 0.15
)

func base() SwitchInputs {
	return SwitchInputs{
		PrevContextTokens: 40000,
		CacheWriteMult:    cw,
		CacheReadMult:     cr,
		ScoreMargin:       margin,
	}
}

func TestDecide_NoPrev(t *testing.T) {
	in := base()
	in.CandidateModel = "claude-opus-4-8"
	m, sw, r := DecideModel(in)
	if m != "claude-opus-4-8" || !sw || r != ReasonNoPrev {
		t.Fatalf("got %q sw=%v r=%s", m, sw, r)
	}
}

func TestDecide_SameModel(t *testing.T) {
	in := base()
	in.PrevModel, in.CandidateModel = "claude-sonnet-5", "claude-sonnet-5"
	in.PrevScore, in.CandidateScore = 0.4, 0.4
	m, sw, r := DecideModel(in)
	if m != "claude-sonnet-5" || sw || r != ReasonSameModel {
		t.Fatalf("got %q sw=%v r=%s", m, sw, r)
	}
}

// Downswitch opus -> haiku: prefix reprocess on haiku (1.25*1.0=1.25) is cheaper
// than reading it on opus (0.10*5.0=0.5)? No: 1.25 > 0.5, so delta > 0 here.
// Use a larger price gap to make the downswitch genuinely cheap on the prefix.
func TestDecide_CheapDownswitch(t *testing.T) {
	in := base()
	in.PrevModel, in.CandidateModel = "claude-opus", "claude-haiku-4-5"
	in.PrevInputPrice, in.CandInputPrice = 15.0, 1.0 // wide gap: 1.25*1 - 0.10*15 = 1.25 - 1.5 < 0
	in.PrevScore, in.CandidateScore = 0.5, 0.45      // small quality change, irrelevant here
	if d := SwitchDeltaUSD(in); d > 0 {
		t.Fatalf("expected negative delta, got %v", d)
	}
	m, sw, r := DecideModel(in)
	if m != "claude-haiku-4-5" || !sw || r != ReasonCheapSwitch {
		t.Fatalf("got %q sw=%v r=%s", m, sw, r)
	}
}

// Upswitch haiku -> opus with a small quality gain below the margin: hold.
func TestDecide_UpswitchHeldBelowMargin(t *testing.T) {
	in := base()
	in.PrevModel, in.CandidateModel = "claude-haiku-4-5", "claude-opus"
	in.PrevInputPrice, in.CandInputPrice = priceHaiku, priceOpus // delta > 0
	in.PrevScore, in.CandidateScore = 0.50, 0.40                 // gain 0.10 < margin 0.15
	if d := SwitchDeltaUSD(in); d <= 0 {
		t.Fatalf("expected positive delta, got %v", d)
	}
	m, sw, r := DecideModel(in)
	if m != "claude-haiku-4-5" || sw || r != ReasonStickyHold {
		t.Fatalf("got %q sw=%v r=%s", m, sw, r)
	}
}

// Upswitch haiku -> opus with a large quality gain above the margin: switch.
func TestDecide_UpswitchQualityOverride(t *testing.T) {
	in := base()
	in.PrevModel, in.CandidateModel = "claude-haiku-4-5", "claude-opus"
	in.PrevInputPrice, in.CandInputPrice = priceHaiku, priceOpus
	in.PrevScore, in.CandidateScore = 0.80, 0.40 // gain 0.40 > margin 0.15
	m, sw, r := DecideModel(in)
	if m != "claude-opus" || !sw || r != ReasonQualityOverride {
		t.Fatalf("got %q sw=%v r=%s", m, sw, r)
	}
}

// Costed switch but the current model is no longer routable: switch.
func TestDecide_PrevUnavailable(t *testing.T) {
	in := base()
	in.PrevModel, in.CandidateModel = "retired-model", "claude-sonnet-5"
	in.PrevInputPrice, in.CandInputPrice = priceHaiku, priceSonn
	in.PrevScore, in.CandidateScore = math.NaN(), 0.42
	m, sw, r := DecideModel(in)
	if m != "claude-sonnet-5" || !sw || r != ReasonPrevUnavailable {
		t.Fatalf("got %q sw=%v r=%s", m, sw, r)
	}
}

// Exactly at the margin (not strictly greater): hold. Uses FP-exact binary
// fractions (0.5 - 0.375 == 0.125 exactly) to test the boundary without
// floating-point noise.
func TestDecide_MarginBoundaryHolds(t *testing.T) {
	in := base()
	in.PrevModel, in.CandidateModel = "claude-haiku-4-5", "claude-opus"
	in.PrevInputPrice, in.CandInputPrice = priceHaiku, priceOpus
	in.ScoreMargin = 0.125
	in.PrevScore, in.CandidateScore = 0.5, 0.375 // gain exactly 0.125 == margin
	m, sw, r := DecideModel(in)
	if sw || r != ReasonStickyHold {
		t.Fatalf("boundary should hold: got %q sw=%v r=%s", m, sw, r)
	}
}
