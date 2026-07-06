package proxy

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/regolo-ai/brick-SR1/apps/router/src/spatial-router/pkg/config"
	"github.com/regolo-ai/brick-SR1/apps/router/src/spatial-router/pkg/sticky"
)

// fakeAnthropicSSE returns an SSE body mimicking a streamed /v1/messages
// response whose usage carries prompt-cache tokens: input 1000 fresh, 39000 read
// from cache, 0 written, 200 output. Total cached prefix = 40000.
func fakeAnthropicSSE() string {
	return "event: message_start\n" +
		`data: {"type":"message_start","message":{"usage":{"input_tokens":1000,"cache_read_input_tokens":39000,"cache_creation_input_tokens":0,"output_tokens":5}}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","usage":{"output_tokens":200}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"
}

// TestStickyEndToEnd drives the full sticky loop through the HTTP forwarder:
// a request is forwarded to a fake Anthropic upstream, the streamed usage
// (including cache tokens) is parsed, the served model is recorded into the
// sticky store, and a subsequent turn's decision reads that state and holds the
// warm model against a cheaper candidate. No docker, no real credentials.
func TestStickyEndToEnd(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, fakeAnthropicSSE())
	}))
	defer upstream.Close()

	s := stickyTestServer(t)
	apCfg := &config.AnthropicPassthroughConfig{
		Enabled:     true,
		UpstreamURL: upstream.URL,
		RoutingMode: config.RoutingModeSticky,
	}

	// Turn 1: identify the conversation, forward to upstream on opus, and let the
	// forwarder record the served model + cached prefix size.
	sys, first := extractAnthropicIdentityParts([]byte(stickyBody))
	key := sticky.HashIdentity(sys, first)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader([]byte(stickyBody)))
	rec := httptest.NewRecorder()
	s.forwardAnthropicRequest(rec, req, apCfg, []byte(stickyBody), "claude-opus-4-8", "hard", "", true, false, key)

	if rec.Code != http.StatusOK {
		t.Fatalf("forward status = %d, want 200", rec.Code)
	}
	entry, ok := s.stickyStore.GetAt(key, time.Now())
	if !ok {
		t.Fatal("turn 1 did not record sticky state")
	}
	if entry.LastModel != "claude-opus-4-8" {
		t.Fatalf("recorded model = %q, want claude-opus-4-8", entry.LastModel)
	}
	if entry.LastContextTokens != 40000 { // 1000 input + 39000 cache_read + 0 cache_creation
		t.Fatalf("recorded context tokens = %d, want 40000", entry.LastContextTokens)
	}

	// Turn 2: the router now prefers the cheaper model, but only a marginal
	// quality change. The recorded warm-on-opus state must make the decision hold
	// opus rather than pay the cache-invalidation cost.
	route := routeWith("claude-haiku-4-5", map[string]float64{
		"claude-haiku-4-5": 0.50,
		"claude-opus-4-8":  0.48, // opus slightly better, well within margin
	})
	_, model, _ := s.applyStickyRouting(apCfg, []byte(stickyBody), route, "claude-haiku-4-5", 0)
	if model != "claude-opus-4-8" {
		t.Fatalf("turn 2 should hold warm opus, got %q", model)
	}
}
