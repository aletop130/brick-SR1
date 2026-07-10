// Package compaction implements Brick's model-agnostic, text-space context
// compaction for the "smartsqueeze" caching mode. When the router switches a
// conversation to a new model, the new provider's KV/prompt cache is cold and
// the whole prefix is reprocessed at full input price. Compacting the prefix
// before forwarding shrinks that reprocessing.
//
// Tier-1 (this file) is deterministic and LLM-free: it replaces the content of
// older tool_result blocks with a fixed placeholder, keeping the most recent
// turns raw. It never touches the system prompt, the first user turn, or any
// text/tool_use/image block, so the sticky conversation identity
// (pkg/sticky.HashIdentity over system + first user turn) is preserved and the
// conversation does not restart cold. See pkg/proxy/anthropic.go and
// pkg/config.AnthropicPassthroughConfig.RoutingMode.
package compaction

import "encoding/json"

// placeholder replaces a cleared tool_result's content. It MUST be a constant:
// any per-turn variation (timestamp, counter) would make the forwarded prefix
// differ turn-over-turn and defeat the new model's warm cache. Mirrors the
// spirit of Anthropic's server-side clear_tool_uses_20250919 placeholder.
const placeholder = "[tool result cleared by Brick]"

// Compact clears the content of tool_result blocks in every message older than
// the last keepRecentTurns messages, replacing each with a short placeholder.
// It returns the rewritten body, an estimate of the tokens saved, and whether
// anything changed.
//
// The contract is deliberately tolerant (mirrors stripUnsupportedFieldsForModel
// in pkg/proxy): on malformed JSON, a missing messages array, or nothing to
// clear, it returns the ORIGINAL body unchanged with changed=false and err=nil.
// A block is cleared only when its current content is larger than the
// placeholder, which makes the operation idempotent (a second Compact clears
// nothing new) and never increases size.
//
// Determinism: the top-level object and each rewritten message are re-marshaled
// from maps (Go emits sorted keys), and untouched messages keep their exact
// bytes via json.RawMessage, so the same input always yields byte-identical
// output.
func Compact(body []byte, keepRecentTurns int) (compacted []byte, estTokensSaved int64, changed bool, err error) {
	if keepRecentTurns < 0 {
		keepRecentTurns = 0
	}

	var top map[string]json.RawMessage
	if e := json.Unmarshal(body, &top); e != nil {
		return body, 0, false, nil
	}
	rawMsgs, ok := top["messages"]
	if !ok {
		return body, 0, false, nil
	}
	var msgs []json.RawMessage
	if e := json.Unmarshal(rawMsgs, &msgs); e != nil {
		return body, 0, false, nil
	}

	// Keep the last keepRecentTurns messages raw; only clear older ones. Counting
	// by message index (not user/assistant "turns") keeps a tool_result whose
	// matching tool_use is still in the retained window untouched, since the pair
	// spans adjacent messages.
	cutoff := len(msgs) - keepRecentTurns
	if cutoff <= 0 {
		return body, 0, false, nil
	}

	var clearedBytes int64
	msgChanged := false
	for i := 0; i < cutoff; i++ {
		newMsg, saved, ok := clearToolResultsInMessage(msgs[i])
		if ok {
			msgs[i] = newMsg
			clearedBytes += saved
			msgChanged = true
		}
	}
	if !msgChanged {
		return body, 0, false, nil
	}

	newMsgs, e := json.Marshal(msgs)
	if e != nil {
		return body, 0, false, nil
	}
	top["messages"] = newMsgs
	out, e := json.Marshal(top)
	if e != nil {
		return body, 0, false, nil
	}
	return out, estTokensFromBytes(clearedBytes), true, nil
}

// clearToolResultsInMessage rewrites the content of every tool_result block in
// one message to the placeholder, when that shrinks it. It preserves all other
// blocks (text, tool_use, image) and all other message/block fields
// (tool_use_id, is_error, ...) byte-for-byte. Returns the message unchanged
// (ok=false) when content is a plain string, is not an array, or holds no
// clearable tool_result.
func clearToolResultsInMessage(raw json.RawMessage) (out json.RawMessage, savedBytes int64, ok bool) {
	var msg map[string]json.RawMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return raw, 0, false
	}
	contentRaw, has := msg["content"]
	if !has {
		return raw, 0, false
	}
	// Polymorphic content: a plain string carries no tool_result, so a failed
	// array decode means "nothing to clear", not an error.
	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(contentRaw, &blocks); err != nil {
		return raw, 0, false
	}

	placeholderRaw, _ := json.Marshal(placeholder)
	var saved int64
	changed := false
	for _, b := range blocks {
		typeRaw, has := b["type"]
		if !has {
			continue
		}
		var typ string
		if json.Unmarshal(typeRaw, &typ) != nil || typ != "tool_result" {
			continue
		}
		old, has := b["content"]
		if !has {
			continue
		}
		// Only clear when it actually shrinks the block. This also makes Compact
		// idempotent: an already-cleared content equals the placeholder length, so
		// the strict > check skips it on a second pass.
		if len(old) <= len(placeholderRaw) {
			continue
		}
		saved += int64(len(old)) - int64(len(placeholderRaw))
		b["content"] = placeholderRaw
		changed = true
	}
	if !changed {
		return raw, 0, false
	}

	newContent, err := json.Marshal(blocks)
	if err != nil {
		return raw, 0, false
	}
	msg["content"] = newContent
	newMsg, err := json.Marshal(msg)
	if err != nil {
		return raw, 0, false
	}
	return newMsg, saved, true
}

// estTokensFromBytes converts cleared UTF-8 bytes to an approximate token count.
// There is no tokenizer in the router (text-space compaction is model-agnostic
// by design), so ~4 chars/token is the honest estimate; treat the result as an
// estimate, not an invoice.
func estTokensFromBytes(n int64) int64 {
	if n <= 0 {
		return 0
	}
	return n / 4
}
