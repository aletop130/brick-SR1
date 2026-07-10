package proxy

import (
	"bytes"
	"testing"
	"time"

	"github.com/regolo-ai/brick-SR1/apps/router/src/spatial-router/pkg/config"
	"github.com/regolo-ai/brick-SR1/apps/router/src/spatial-router/pkg/sticky"
)

// A conversation body with an old, clearable tool_result plus recent turns.
const squeezeBody = `{"system":"you are a helpful assistant","messages":[` +
	`{"role":"user","content":"the first user turn"},` +
	`{"role":"assistant","content":[{"type":"tool_use","id":"tu_1","name":"read","input":{}}]},` +
	`{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_1","content":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}]},` +
	`{"role":"assistant","content":"ok"},` +
	`{"role":"user","content":"next question please"}` +
	`]}`

func squeezeCfg(shadow bool) *config.AnthropicPassthroughConfig {
	return &config.AnthropicPassthroughConfig{
		RoutingMode:            config.RoutingModeSmartSqueeze,
		CompactKeepRecentTurns: 2,
		CompactShadowOnly:      &shadow,
	}
}

func seedPrev(t *testing.T, s *Server, model string, compacted bool) string {
	t.Helper()
	sys, first := extractAnthropicIdentityParts([]byte(squeezeBody))
	key := sticky.HashIdentity(sys, first)
	s.stickyStore.Record(key, model, 40000, compacted, time.Now())
	return key
}

func TestSmartSqueeze_ServesCompactedOnSwitch(t *testing.T) {
	s := stickyTestServer(t)
	key := seedPrev(t, s, "claude-opus-4-8", false) // prev opus, now switching to haiku
	ev := &routingEvent{}

	out, compacted := s.applySmartSqueeze(squeezeCfg(false), []byte(squeezeBody), key, "claude-haiku-4-5", ev)
	if !compacted {
		t.Fatal("expected compacted=true on a served switch")
	}
	if bytes.Equal(out, []byte(squeezeBody)) {
		t.Fatal("expected a compacted (different) body")
	}
	if bytes.Contains(out, []byte("AAAAAAAA")) {
		t.Fatal("old tool_result should have been cleared")
	}
	if ev.EstSavedTokens <= 0 {
		t.Fatalf("expected EstSavedTokens > 0, got %d", ev.EstSavedTokens)
	}
	// Identity must survive so the sticky key stays stable.
	sysIn, firstIn := extractAnthropicIdentityParts([]byte(squeezeBody))
	sysOut, firstOut := extractAnthropicIdentityParts(out)
	if sysIn != sysOut || firstIn != firstOut {
		t.Fatal("compaction perturbed the conversation identity")
	}
}

func TestSmartSqueeze_HoldForwardsRaw(t *testing.T) {
	s := stickyTestServer(t)
	// Same model as the served one: not a switch, must forward raw.
	key := seedPrev(t, s, "claude-haiku-4-5", false)
	ev := &routingEvent{}

	out, compacted := s.applySmartSqueeze(squeezeCfg(false), []byte(squeezeBody), key, "claude-haiku-4-5", ev)
	if compacted {
		t.Fatal("same-model turn must not compact")
	}
	if !bytes.Equal(out, []byte(squeezeBody)) {
		t.Fatal("expected raw body on a hold / same-model turn")
	}
	if ev.EstSavedTokens != 0 {
		t.Fatalf("expected no saving recorded on hold, got %d", ev.EstSavedTokens)
	}
}

func TestSmartSqueeze_ShadowForwardsRawButMeasures(t *testing.T) {
	s := stickyTestServer(t)
	key := seedPrev(t, s, "claude-opus-4-8", false) // switch away from opus
	ev := &routingEvent{}

	out, compacted := s.applySmartSqueeze(squeezeCfg(true), []byte(squeezeBody), key, "claude-haiku-4-5", ev)
	if compacted {
		t.Fatal("shadow sub-mode must not serve compacted")
	}
	if !bytes.Equal(out, []byte(squeezeBody)) {
		t.Fatal("shadow sub-mode must forward the raw body")
	}
	if ev.EstSavedTokens <= 0 {
		t.Fatalf("shadow sub-mode should still measure EstSavedTokens, got %d", ev.EstSavedTokens)
	}
	if ev.ShadowNote != "smartsqueeze_shadow" {
		t.Fatalf("expected shadow note, got %q", ev.ShadowNote)
	}
}

func TestSmartSqueeze_ContinuesWhileCompacted(t *testing.T) {
	s := stickyTestServer(t)
	// No switch (same model) but the conversation is already compacted: keep
	// clearing so the new model's cache stays warm on the reduced prefix.
	key := seedPrev(t, s, "claude-haiku-4-5", true)
	ev := &routingEvent{}

	out, compacted := s.applySmartSqueeze(squeezeCfg(false), []byte(squeezeBody), key, "claude-haiku-4-5", ev)
	if !compacted {
		t.Fatal("expected continued compaction while the conversation is compacted")
	}
	if bytes.Contains(out, []byte("AAAAAAAA")) {
		t.Fatal("old tool_result should have been cleared")
	}
}

func TestSmartSqueeze_NoPrevForwardsRaw(t *testing.T) {
	s := stickyTestServer(t)
	// Identifiable body but no prior turn recorded: first turn, nothing to switch.
	sys, first := extractAnthropicIdentityParts([]byte(squeezeBody))
	key := sticky.HashIdentity(sys, first)
	ev := &routingEvent{}

	out, compacted := s.applySmartSqueeze(squeezeCfg(false), []byte(squeezeBody), key, "claude-haiku-4-5", ev)
	if compacted || !bytes.Equal(out, []byte(squeezeBody)) {
		t.Fatal("first turn (no prev) must forward raw")
	}
}
