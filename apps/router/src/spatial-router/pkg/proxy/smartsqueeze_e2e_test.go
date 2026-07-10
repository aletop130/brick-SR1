package proxy

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/regolo-ai/brick-SR1/apps/router/src/spatial-router/pkg/config"
	"github.com/regolo-ai/brick-SR1/apps/router/src/spatial-router/pkg/sticky"
)

// TestSmartSqueezeEndToEnd drives the full smartsqueeze loop through the HTTP
// forwarder: a conversation warm on opus switches to haiku, the compactor clears
// the old tool_result before forwarding, and the fake upstream captures the body
// it actually receives. Asserts the upstream got the reduced prefix and that the
// conversation is flagged compacted for the next turn. No docker, no creds.
func TestSmartSqueezeEndToEnd(t *testing.T) {
	var gotBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, fakeAnthropicSSE())
	}))
	defer upstream.Close()

	s := stickyTestServer(t)
	shadow := false // served mode
	apCfg := &config.AnthropicPassthroughConfig{
		Enabled:                true,
		UpstreamURL:            upstream.URL,
		RoutingMode:            config.RoutingModeSmartSqueeze,
		CompactKeepRecentTurns: 2,
		CompactShadowOnly:      &shadow,
	}

	// Seed the conversation warm on opus (earlier arrival) so routing to haiku
	// this turn is a genuine switch.
	sys, first := extractAnthropicIdentityParts([]byte(squeezeBody))
	key := sticky.HashIdentity(sys, first)
	s.stickyStore.Record(key, "claude-opus-4-8", 40000, false, time.Now().Add(-time.Second))

	ev := &routingEvent{}
	body, compacted := s.applySmartSqueeze(apCfg, []byte(squeezeBody), key, "claude-haiku-4-5", ev)
	if !compacted {
		t.Fatal("expected served compaction on a switch turn")
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.forwardAnthropicRequest(rec, req, apCfg, body, "claude-haiku-4-5", "easy", "", true, false, key, compacted, ev)

	if rec.Code != http.StatusOK {
		t.Fatalf("forward status = %d, want 200", rec.Code)
	}
	// The upstream must have received the compacted prefix, not the raw one.
	if bytes.Contains(gotBody, []byte("AAAAAAAA")) {
		t.Fatal("upstream received the un-compacted old tool_result")
	}
	if !bytes.Contains(gotBody, []byte("[tool result cleared by Brick]")) {
		t.Fatal("upstream did not receive the compaction placeholder")
	}
	// Next-turn state: served model recorded and flagged compacted so the reduced
	// prefix keeps being sent while on this model.
	entry, ok := s.stickyStore.GetAt(key, time.Now())
	if !ok {
		t.Fatal("expected sticky state recorded after forward")
	}
	if entry.LastModel != "claude-haiku-4-5" {
		t.Fatalf("recorded model = %q, want claude-haiku-4-5", entry.LastModel)
	}
	if !entry.Compacted {
		t.Fatal("expected Compacted=true recorded for the next turn")
	}
}

// TestSmartSqueezeShadowEndToEnd verifies that in shadow sub-mode the upstream
// receives the RAW body (no behavior change) even though the saving is measured.
func TestSmartSqueezeShadowEndToEnd(t *testing.T) {
	var gotBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, fakeAnthropicSSE())
	}))
	defer upstream.Close()

	s := stickyTestServer(t)
	shadow := true // shadow-only
	apCfg := &config.AnthropicPassthroughConfig{
		Enabled:                true,
		UpstreamURL:            upstream.URL,
		RoutingMode:            config.RoutingModeSmartSqueeze,
		CompactKeepRecentTurns: 2,
		CompactShadowOnly:      &shadow,
	}

	sys, first := extractAnthropicIdentityParts([]byte(squeezeBody))
	key := sticky.HashIdentity(sys, first)
	s.stickyStore.Record(key, "claude-opus-4-8", 40000, false, time.Now().Add(-time.Second))

	ev := &routingEvent{}
	body, compacted := s.applySmartSqueeze(apCfg, []byte(squeezeBody), key, "claude-haiku-4-5", ev)
	if compacted {
		t.Fatal("shadow sub-mode must not serve compacted")
	}
	if ev.EstSavedTokens <= 0 {
		t.Fatalf("shadow should still measure a saving, got %d", ev.EstSavedTokens)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.forwardAnthropicRequest(rec, req, apCfg, body, "claude-haiku-4-5", "easy", "", true, false, key, compacted, ev)

	if !bytes.Contains(gotBody, []byte("AAAAAAAA")) {
		t.Fatal("shadow mode altered the forwarded body; upstream should get the raw prefix")
	}
	entry, _ := s.stickyStore.GetAt(key, time.Now())
	if entry.Compacted {
		t.Fatal("shadow mode must not flag the conversation compacted")
	}
}
