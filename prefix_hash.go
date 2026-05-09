package main

import (
	"hash/fnv"
)

// ChunkedPrefixHash converts a request's flattened prompt into a chain of
// fixed-size hash chunks. Each chunk represents `chunkTokens` worth of
// prompt content; downstream callers compare these chains across
// backends to find prefix overlaps.
//
// We don't have a real tokenizer in the router (we'd need to ship one
// per provider, and they disagree). Instead we treat 4 bytes ≈ 1 token
// as a stable approximation. This is empirically within 10-20% of GPT
// tokenisation and that's enough for prefix matching, where the goal is
// "did we serve a similar prefix before", not exact token boundaries.
//
// Returned hashes are FNV-1a 64-bit on each chunk's content. FNV is fast,
// stdlib, and deterministic within a process; collision probability for
// even a million prefixes is well under 1e-12 at 64 bits.
func ChunkedPrefixHash(prompt string, chunkTokens int) []uint64 {
	if prompt == "" || chunkTokens <= 0 {
		return nil
	}
	chunkBytes := chunkTokens * 4 // 4 bytes ≈ 1 token approximation
	if chunkBytes < 16 {
		chunkBytes = 16
	}
	hashes := make([]uint64, 0, len(prompt)/chunkBytes+1)
	for i := 0; i < len(prompt); i += chunkBytes {
		end := i + chunkBytes
		if end > len(prompt) {
			end = len(prompt)
		}
		h := fnv.New64a()
		_, _ = h.Write([]byte(prompt[i:end]))
		hashes = append(hashes, h.Sum64())
	}
	return hashes
}

// FlattenMessages produces the canonical string used for prefix hashing.
// Order matters; the same conversation produces the same hash chain.
//
// Content can be either a plain string or an array of ContentPart objects
// (multi-modal). For prefix hashing we only care about the text; image
// parts are summarised by their URL hash (so identical image references
// produce identical prefix hashes, but the image bytes never get walked).
//
// Tool calls and other non-text content are omitted because they're
// usually agent-loop chatter that doesn't compress well into KV anyway.
func FlattenMessages(messages []ChatMessage) string {
	if len(messages) == 0 {
		return ""
	}
	var b []byte
	for _, m := range messages {
		b = append(b, m.Role...)
		b = append(b, ':', '\n')
		b = appendContentText(b, m.Content)
		b = append(b, '\n', '\n')
	}
	return string(b)
}

func appendContentText(b []byte, content interface{}) []byte {
	switch v := content.(type) {
	case string:
		return append(b, v...)
	case []ContentPart:
		for _, p := range v {
			if p.Text != "" {
				b = append(b, p.Text...)
			} else if p.ImageURL != nil && p.ImageURL.URL != "" {
				// Hashes equally for identical image references.
				b = append(b, "[image:"...)
				b = append(b, p.ImageURL.URL...)
				b = append(b, ']')
			}
			b = append(b, '\n')
		}
		return b
	case []interface{}:
		// JSON-decoded multi-modal: array of map[string]interface{}.
		for _, raw := range v {
			if m, ok := raw.(map[string]interface{}); ok {
				if t, ok := m["text"].(string); ok && t != "" {
					b = append(b, t...)
				} else if iu, ok := m["image_url"].(map[string]interface{}); ok {
					if url, ok := iu["url"].(string); ok && url != "" {
						b = append(b, "[image:"...)
						b = append(b, url...)
						b = append(b, ']')
					}
				}
				b = append(b, '\n')
			}
		}
		return b
	default:
		return b
	}
}
