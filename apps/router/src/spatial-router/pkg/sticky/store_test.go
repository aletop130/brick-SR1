package sticky

import (
	"testing"
	"time"
)

func TestRecordAndGet(t *testing.T) {
	s := New(6 * time.Minute)
	t0 := time.Unix(1000, 0)
	s.Record("conv-a", "claude-opus-4-8", 12000, t0)

	e, ok := s.GetAt("conv-a", t0.Add(time.Minute))
	if !ok {
		t.Fatal("expected entry present")
	}
	if e.LastModel != "claude-opus-4-8" {
		t.Fatalf("LastModel = %q, want claude-opus-4-8", e.LastModel)
	}
	if e.LastContextTokens != 12000 {
		t.Fatalf("LastContextTokens = %d, want 12000", e.LastContextTokens)
	}
}

func TestGetMissing(t *testing.T) {
	s := New(6 * time.Minute)
	if _, ok := s.GetAt("nope", time.Unix(1000, 0)); ok {
		t.Fatal("expected miss on empty store")
	}
}

func TestExpiryTreatedAsAbsent(t *testing.T) {
	s := New(6 * time.Minute)
	t0 := time.Unix(1000, 0)
	s.Record("conv-a", "claude-haiku-4-5", 500, t0)

	// Just before TTL: present.
	if _, ok := s.GetAt("conv-a", t0.Add(6*time.Minute)); !ok {
		t.Fatal("expected entry still live at exactly TTL")
	}
	// Past TTL: absent (cold cache -> free switch).
	if _, ok := s.GetAt("conv-a", t0.Add(6*time.Minute+time.Second)); ok {
		t.Fatal("expected entry expired past TTL")
	}
	// Expired entry should be lazily evicted.
	if s.Len() != 0 {
		t.Fatalf("expected 0 live entries after expiry eviction, got %d", s.Len())
	}
}

// TestLastWriteWinsOnArrival covers the concurrency edge the review flagged:
// two SSE responses on one conversation can complete out of order. Ordering on
// response-arrival time (not request-send time) must keep the freshest state.
func TestLastWriteWinsOnArrival(t *testing.T) {
	s := New(6 * time.Minute)
	base := time.Unix(1000, 0)
	newer := base.Add(2 * time.Second)
	older := base.Add(1 * time.Second)

	// The turn that arrived later is recorded first.
	s.Record("conv-a", "claude-opus-4-8", 40000, newer)
	// A turn that arrived earlier completes its bookkeeping afterwards: must NOT
	// overwrite the fresher entry.
	s.Record("conv-a", "claude-haiku-4-5", 500, older)

	e, ok := s.GetAt("conv-a", newer.Add(time.Minute))
	if !ok {
		t.Fatal("expected entry present")
	}
	if e.LastModel != "claude-opus-4-8" || e.LastContextTokens != 40000 {
		t.Fatalf("stale write won: got %q/%d, want claude-opus-4-8/40000", e.LastModel, e.LastContextTokens)
	}
	if !e.LastSeen.Equal(newer) {
		t.Fatalf("LastSeen = %v, want %v (newest arrival)", e.LastSeen, newer)
	}
}

func TestRecordEqualTimestampIgnored(t *testing.T) {
	s := New(6 * time.Minute)
	t0 := time.Unix(1000, 0)
	s.Record("conv-a", "claude-opus-4-8", 40000, t0)
	// Same arrival timestamp: treated as not-newer, first write wins.
	s.Record("conv-a", "claude-haiku-4-5", 500, t0)

	e, _ := s.GetAt("conv-a", t0.Add(time.Minute))
	if e.LastModel != "claude-opus-4-8" {
		t.Fatalf("equal-timestamp write overwrote: got %q", e.LastModel)
	}
}

func TestHashIdentityStableAndDistinct(t *testing.T) {
	a := HashIdentity("you are a helpful assistant", "first question")
	b := HashIdentity("you are a helpful assistant", "first question")
	if a != b {
		t.Fatalf("hash not stable: %q != %q", a, b)
	}
	c := HashIdentity("you are a helpful assistant", "different first question")
	if a == c {
		t.Fatal("distinct first turns produced same hash")
	}
	d := HashIdentity("different system", "first question")
	if a == d {
		t.Fatal("distinct system prompts produced same hash")
	}
}
