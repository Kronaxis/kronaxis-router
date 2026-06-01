package main

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
)

// JSON structural compression. This is a clean-room Go reimplementation of the
// idea behind headroom's "SmartCrusher" (https://github.com/chopratejas/headroom,
// Apache-2.0): structured data dominates agent token spend and pretty-printed
// JSON is mostly insignificant whitespace. We re-encode it compactly, and
// optionally prune null/empty fields. No headroom source was copied; see NOTICE.
//
// crushJSON is near-lossless by default: it only strips insignificant whitespace
// and re-encodes. Map key order is normalised (Go marshals maps sorted), which
// is irrelevant to an LLM reading the data. With dropNulls=true it also removes
// keys whose value is null or an empty object/array/string — a lossy step the
// caller opts into for the bulk/background compress path.

// looksLikeJSON reports whether s (after trimming) is a self-contained JSON
// object or array. Cheap structural check first, then a full validity parse.
func looksLikeJSON(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) < 2 {
		return false
	}
	first, last := s[0], s[len(s)-1]
	if !((first == '{' && last == '}') || (first == '[' && last == ']')) {
		return false
	}
	return json.Valid([]byte(s))
}

// crushJSON compacts a JSON document. Returns (compacted, true) on success, or
// ("", false) if s is not valid JSON (caller should fall back to a text path).
func crushJSON(s string, dropNulls bool) (string, bool) {
	return crushJSONOpts(s, dropNulls, false)
}

// crushJSONOpts is crushJSON with optional array-of-objects tabularisation.
func crushJSONOpts(s string, dropNulls, tabularize bool) (string, bool) {
	s = strings.TrimSpace(s)
	var v interface{}
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return "", false
	}
	if dropNulls {
		v = pruneJSON(v)
	}
	if tabularize {
		// Union (sparse-key) tabularisation is only enabled alongside null
		// pruning: once nulls are pruned, a null cell unambiguously means "this
		// record lacked this key", keeping the form reversible.
		v = tabularizeJSON(v, dropNulls)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	// Keep <, >, & literal — escaping them inflates the output and changes how
	// embedded code/markup reads to the model.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", false
	}
	// json.Encoder always appends a newline; drop it.
	return strings.TrimRight(buf.String(), "\n"), true
}

// pruneJSON recursively removes map keys whose value is null or an empty
// container/string. Array elements are kept (dropping them would shift indices
// and change counts the model may rely on); their contents are still pruned.
func pruneJSON(v interface{}) interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(t))
		for k, val := range t {
			pv := pruneJSON(val)
			if isEmptyJSON(pv) {
				continue
			}
			out[k] = pv
		}
		return out
	case []interface{}:
		out := make([]interface{}, 0, len(t))
		for _, val := range t {
			out = append(out, pruneJSON(val))
		}
		return out
	default:
		return v
	}
}

func isEmptyJSON(v interface{}) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case map[string]interface{}:
		return len(t) == 0
	case []interface{}:
		return len(t) == 0
	default:
		return false
	}
}

// tabularizeJSON rewrites arrays of uniform objects (same key set, repeated) as
// {"__cols__":[...keys],"__rows__":[[...values]...]}, hoisting each repeated key
// name out once instead of per element. This is the big win on tool outputs and
// API responses — arrays of records dominate token spend and repeat their keys
// on every row. It is lossless and reversible (a reader, human or model, can
// reconstruct the records by zipping cols with each row). Applied recursively.
//
// minTabularRows is the threshold below which hoisting does not pay for the
// __cols__/__rows__ scaffolding.
const minTabularRows = 3

func tabularizeJSON(v interface{}, union bool) interface{} {
	switch t := v.(type) {
	case []interface{}:
		for i := range t {
			t[i] = tabularizeJSON(t[i], union)
		}
		if cols, ok := tabularizableArray(t, union); ok {
			rows := make([]interface{}, len(t))
			for i, el := range t {
				m := el.(map[string]interface{})
				row := make([]interface{}, len(cols))
				for j, c := range cols {
					row[j] = m[c] // nil when absent (union mode); always present in strict mode
				}
				rows[i] = row
			}
			colsIface := make([]interface{}, len(cols))
			for i, c := range cols {
				colsIface[i] = c
			}
			return map[string]interface{}{"__cols__": colsIface, "__rows__": rows}
		}
		return t
	case map[string]interface{}:
		for k := range t {
			t[k] = tabularizeJSON(t[k], union)
		}
		return t
	default:
		return v
	}
}

// tabularizableArray reports whether arr is worth tabularising and returns the
// sorted column names. Strict mode (union=false) requires every element to have
// the identical key set. Union mode (union=true) allows sparse records, filling
// missing cells with null, but only when the records are mostly similar — the
// key union must not exceed 2× the smallest record's key count, otherwise the
// __cols__/__rows__ scaffolding plus null padding would not pay off.
func tabularizableArray(arr []interface{}, union bool) ([]string, bool) {
	if len(arr) < minTabularRows {
		return nil, false
	}
	first, ok := arr[0].(map[string]interface{})
	if !ok || len(first) == 0 {
		return nil, false
	}

	keySet := make(map[string]struct{}, len(first))
	for k := range first {
		keySet[k] = struct{}{}
	}
	want := sortedKey(first)
	minKeys := len(first)

	for _, el := range arr[1:] {
		m, ok := el.(map[string]interface{})
		if !ok || len(m) == 0 {
			return nil, false
		}
		if !union {
			if len(m) != len(first) || sortedKey(m) != want {
				return nil, false
			}
			continue
		}
		if len(m) < minKeys {
			minKeys = len(m)
		}
		for k := range m {
			keySet[k] = struct{}{}
		}
	}

	if union && len(keySet) > 2*minKeys {
		return nil, false // too heterogeneous to pay off
	}

	cols := make([]string, 0, len(keySet))
	for k := range keySet {
		cols = append(cols, k)
	}
	sort.Strings(cols)
	return cols, true
}

func sortedKey(m map[string]interface{}) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, "\x00")
}
