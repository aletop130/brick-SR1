package proxy

import (
	"bufio"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"os"
	"sort"

	"github.com/regolo-ai/brick-SR1/apps/router/src/spatial-router/pkg/observability/logging"
)

// routingModeStats is the aggregate for one routing mode over the whole event
// log. Median switch delta is reported both overall and restricted to held
// turns (CandidateModel != ServedModel), since only a hold realizes the avoided
// prefix-reprocessing cost; the held figure is the honest "savings from sticky"
// number. Latency percentiles are Brick's end-to-end view (nearest-rank).
type routingModeStats struct {
	Mode                  string  `json:"mode"`
	Requests              int     `json:"requests"`
	Sessions              int     `json:"sessions"`
	HeldRequests          int     `json:"held_requests"`
	LatencyP50Ms          int64   `json:"latency_p50_ms"`
	LatencyP95Ms          int64   `json:"latency_p95_ms"`
	MedianSwitchDelta     float64 `json:"median_switch_delta_price_units"`
	MedianSwitchDeltaHeld float64 `json:"median_switch_delta_price_units_held"`
}

// routingStatsResponse is the /api/v1/routing/stats payload: one row per mode
// observed in the log, plus totals. Designed to be safely pollable at any point
// (empty log -> 200 with an explanatory note, never an error), mirroring the
// economics endpoint's tolerance.
type routingStatsResponse struct {
	Modes         []routingModeStats `json:"modes"`
	TotalRequests int                `json:"total_requests"`
	TotalSessions int                `json:"total_sessions"`
	Note          string             `json:"note,omitempty"`
}

// handleRoutingStats reads the append-only routing event log and returns the
// per-mode promotion-gate aggregates: distinct dev sessions (by session_key),
// median realized savings (switch delta on held turns), and p50/p95 end-to-end
// latency. GET-only. Reads the file fresh on every call so it reflects the log
// written by the same process (the writer holds a separate append handle).
func (s *Server) handleRoutingStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s.routingEventPath == "" {
		writeJSON(w, http.StatusOK, routingStatsResponse{Note: "routing event log not configured"})
		return
	}

	f, err := os.Open(s.routingEventPath)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusOK, routingStatsResponse{Note: "no routing events recorded yet"})
			return
		}
		logging.Warnf("routing stats: cannot read %s: %v", s.routingEventPath, err)
		writeJSON(w, http.StatusOK, routingStatsResponse{Note: "routing event log unreadable"})
		return
	}
	defer f.Close()

	writeJSON(w, http.StatusOK, aggregateRoutingEvents(f))
}

// routingModeAcc accumulates one mode's events before summarization.
type routingModeAcc struct {
	sessions   map[string]struct{}
	latencies  []int64
	deltas     []float64
	deltasHeld []float64
	requests   int
	held       int
}

// aggregateRoutingEvents parses a JSONL routing event stream (one routingEvent
// per line, malformed lines skipped) and summarizes it per mode. Kept separate
// from the handler so it can be unit-tested on a plain reader.
func aggregateRoutingEvents(r io.Reader) routingStatsResponse {
	byMode := map[string]*routingModeAcc{}
	allSessions := map[string]struct{}{}
	total := 0

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // tolerate long lines
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev routingEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		total++
		acc := byMode[ev.Mode]
		if acc == nil {
			acc = &routingModeAcc{sessions: map[string]struct{}{}}
			byMode[ev.Mode] = acc
		}
		acc.requests++
		if ev.SessionKey != "" {
			acc.sessions[ev.SessionKey] = struct{}{}
			allSessions[ev.SessionKey] = struct{}{}
		}
		acc.latencies = append(acc.latencies, ev.E2ELatencyMs)
		acc.deltas = append(acc.deltas, ev.EstSwitchDelta)
		// A held turn is where sticky replaced the candidate: candidate != served
		// (both known). Only these realize the avoided reprocessing cost.
		if ev.CandidateModel != "" && ev.ServedModel != "" && ev.CandidateModel != ev.ServedModel {
			acc.held++
			acc.deltasHeld = append(acc.deltasHeld, ev.EstSwitchDelta)
		}
	}

	modes := make([]routingModeStats, 0, len(byMode))
	for mode, acc := range byMode {
		modes = append(modes, routingModeStats{
			Mode:                  mode,
			Requests:              acc.requests,
			Sessions:              len(acc.sessions),
			HeldRequests:          acc.held,
			LatencyP50Ms:          percentileInt64(acc.latencies, 0.50),
			LatencyP95Ms:          percentileInt64(acc.latencies, 0.95),
			MedianSwitchDelta:     medianFloat(acc.deltas),
			MedianSwitchDeltaHeld: medianFloat(acc.deltasHeld),
		})
	}
	sort.Slice(modes, func(i, j int) bool { return modes[i].Mode < modes[j].Mode })

	return routingStatsResponse{
		Modes:         modes,
		TotalRequests: total,
		TotalSessions: len(allSessions),
	}
}

// percentileInt64 returns the nearest-rank q-percentile of vals (0 for empty).
// Sorts a copy so the caller's slice order is preserved.
func percentileInt64(vals []int64, q float64) int64 {
	if len(vals) == 0 {
		return 0
	}
	s := append([]int64(nil), vals...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	rank := int(math.Ceil(q*float64(len(s)))) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(s) {
		rank = len(s) - 1
	}
	return s[rank]
}

// medianFloat returns the median of vals (0 for empty), sorting a copy.
func medianFloat(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	s := append([]float64(nil), vals...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}
