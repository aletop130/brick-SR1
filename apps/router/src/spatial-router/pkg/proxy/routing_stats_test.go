package proxy

import (
	"strings"
	"testing"
)

// TestAggregateRoutingEvents checks the per-mode summarization: distinct
// sessions, held-turn detection (candidate != served), nearest-rank latency
// percentiles, and overall vs held median switch delta. A malformed line is
// skipped rather than aborting the aggregate.
func TestAggregateRoutingEvents(t *testing.T) {
	log := strings.Join([]string{
		`{"mode":"sticky","session_key":"s1","candidate_model":"claude-haiku-4-5","served_model":"claude-opus-4-8","est_switch_delta_price_units":5.0,"e2e_latency_ms":100}`,
		`{"mode":"sticky","session_key":"s1","candidate_model":"claude-opus-4-8","served_model":"claude-opus-4-8","est_switch_delta_price_units":0,"e2e_latency_ms":200}`,
		`{"mode":"sticky","session_key":"s2","candidate_model":"claude-haiku-4-5","served_model":"claude-haiku-4-5","est_switch_delta_price_units":1.0,"e2e_latency_ms":300}`,
		`{ this is not json }`,
		`{"mode":"off","session_key":"s3","candidate_model":"claude-haiku-4-5","served_model":"claude-haiku-4-5","est_switch_delta_price_units":0,"e2e_latency_ms":50}`,
		``,
	}, "\n")

	got := aggregateRoutingEvents(strings.NewReader(log))

	if got.TotalRequests != 4 {
		t.Fatalf("total_requests = %d, want 4 (malformed line skipped)", got.TotalRequests)
	}
	if got.TotalSessions != 3 {
		t.Fatalf("total_sessions = %d, want 3 (s1,s2,s3)", got.TotalSessions)
	}
	if len(got.Modes) != 2 {
		t.Fatalf("got %d modes, want 2 (off,sticky)", len(got.Modes))
	}

	// Modes are sorted alphabetically: off, sticky.
	off, sticky := got.Modes[0], got.Modes[1]
	if off.Mode != "off" || sticky.Mode != "sticky" {
		t.Fatalf("mode order = %q,%q, want off,sticky", off.Mode, sticky.Mode)
	}

	if sticky.Requests != 3 || sticky.Sessions != 2 || sticky.HeldRequests != 1 {
		t.Fatalf("sticky agg = req %d sess %d held %d, want 3/2/1", sticky.Requests, sticky.Sessions, sticky.HeldRequests)
	}
	// latencies [100,200,300]: nearest-rank p50 -> 200, p95 -> 300.
	if sticky.LatencyP50Ms != 200 || sticky.LatencyP95Ms != 300 {
		t.Fatalf("sticky latency p50/p95 = %d/%d, want 200/300", sticky.LatencyP50Ms, sticky.LatencyP95Ms)
	}
	// deltas [0,1,5] -> median 1.0; held-only [5] -> 5.0.
	if sticky.MedianSwitchDelta != 1.0 || sticky.MedianSwitchDeltaHeld != 5.0 {
		t.Fatalf("sticky median delta/held = %.2f/%.2f, want 1.00/5.00", sticky.MedianSwitchDelta, sticky.MedianSwitchDeltaHeld)
	}

	if off.Requests != 1 || off.Sessions != 1 || off.HeldRequests != 0 {
		t.Fatalf("off agg = req %d sess %d held %d, want 1/1/0", off.Requests, off.Sessions, off.HeldRequests)
	}
	if off.LatencyP50Ms != 50 || off.MedianSwitchDeltaHeld != 0 {
		t.Fatalf("off p50/held-median = %d/%.2f, want 50/0.00", off.LatencyP50Ms, off.MedianSwitchDeltaHeld)
	}
}
