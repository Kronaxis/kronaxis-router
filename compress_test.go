package main

import (
	"strings"
	"testing"
)

func TestCrushJSON(t *testing.T) {
	pretty := `{
    "name":  "alice",
    "age":   30,
    "tags": [
        "a",
        "b"
    ]
}`
	got, ok := crushJSON(pretty, false)
	if !ok {
		t.Fatal("crushJSON returned ok=false for valid JSON")
	}
	if strings.ContainsAny(got, "\n") || strings.Contains(got, "  ") {
		t.Errorf("expected compact JSON, got %q", got)
	}
	for _, want := range []string{`"name":"alice"`, `"age":30`, `"tags":["a","b"]`} {
		if !strings.Contains(got, want) {
			t.Errorf("compact JSON missing %q; got %q", want, got)
		}
	}
}

func TestCrushJSONInvalid(t *testing.T) {
	if _, ok := crushJSON(`{not json`, false); ok {
		t.Error("expected ok=false for invalid JSON")
	}
	if _, ok := crushJSON(`just prose`, false); ok {
		t.Error("expected ok=false for prose")
	}
}

func TestCrushJSONDropNulls(t *testing.T) {
	in := `{"keep":1,"drop":null,"empty":"","emptyobj":{},"emptyarr":[],"nested":{"x":null,"y":2}}`
	got, ok := crushJSON(in, true)
	if !ok {
		t.Fatal("crushJSON ok=false")
	}
	for _, bad := range []string{`"drop"`, `"empty"`, `"emptyobj"`, `"emptyarr"`, `"x"`} {
		if strings.Contains(got, bad) {
			t.Errorf("dropNulls should have removed %s; got %q", bad, got)
		}
	}
	for _, want := range []string{`"keep":1`, `"y":2`} {
		if !strings.Contains(got, want) {
			t.Errorf("dropNulls removed something it should keep (%s); got %q", want, got)
		}
	}
}

func TestCrushJSONNoHTMLEscape(t *testing.T) {
	got, ok := crushJSON(`{"html":"a < b && c > d"}`, false)
	if !ok {
		t.Fatal("ok=false")
	}
	// The \uXXXX escaped forms must not appear; the literal characters must survive.
	for _, escSeq := range []string{"\\u003c", "\\u003e", "\\u0026"} {
		if strings.Contains(got, escSeq) {
			t.Errorf("HTML char was escaped (%s); got %q", escSeq, got)
		}
	}
	if !strings.Contains(got, "a < b && c > d") {
		t.Errorf("expected literal HTML chars preserved; got %q", got)
	}
}

func TestStripCommentsGo(t *testing.T) {
	src := `package main

// a leading comment
func f() {
	x := 1 // trailing comment
	s := "not // a comment"
	r := ` + "`raw // string`" + `
	/* block
	   comment */
	return x
}`
	got := compressCode(src, "go")
	if strings.Contains(got, "leading comment") {
		t.Error("leading comment not stripped")
	}
	if strings.Contains(got, "trailing comment") {
		t.Error("trailing comment not stripped")
	}
	if strings.Contains(got, "block") || strings.Contains(got, "comment */") {
		t.Error("block comment not stripped")
	}
	if !strings.Contains(got, `"not // a comment"`) {
		t.Errorf("string literal containing // was corrupted; got:\n%s", got)
	}
	if !strings.Contains(got, "`raw // string`") {
		t.Errorf("raw string literal was corrupted; got:\n%s", got)
	}
	if !strings.Contains(got, "x := 1") {
		t.Errorf("code line lost when stripping trailing comment; got:\n%s", got)
	}
}

func TestStripCommentsPython(t *testing.T) {
	src := `# header comment
def f():
    x = 1  # set x
    s = "# not a comment"
    doc = """
    # also not a comment
    """
    return x`
	got := compressCode(src, "python")
	if strings.Contains(got, "header comment") || strings.Contains(got, "set x") {
		t.Errorf("python comments not stripped; got:\n%s", got)
	}
	if !strings.Contains(got, `"# not a comment"`) {
		t.Errorf("string with # corrupted; got:\n%s", got)
	}
	if !strings.Contains(got, "# also not a comment") {
		t.Errorf("triple-quoted string with # corrupted; got:\n%s", got)
	}
}

func TestCompressCodeBashUnchanged(t *testing.T) {
	// Bash is deliberately NOT comment-stripped: `#` is ambiguous.
	src := "x=${var#prefix}  # real comment\necho $x"
	got := compressCode(src, "bash")
	if got != src {
		t.Errorf("bash should be left unchanged to avoid corrupting ${var#...}; got %q", got)
	}
}

func TestCompressCodeUnknownLangUnchanged(t *testing.T) {
	src := "some // thing\nelse"
	if got := compressCode(src, "brainfuck"); got != src {
		t.Errorf("unknown language should be unchanged; got %q", got)
	}
}

func TestSplitFences(t *testing.T) {
	text := "intro prose\n```go\nfmt.Println()\n```\nmiddle\n```json\n{}\n```\nend"
	segs := splitFences(text)
	var kinds []segKind
	for _, s := range segs {
		kinds = append(kinds, s.kind)
	}
	want := []segKind{kindProse, kindCode, kindProse, kindCode, kindProse}
	if len(kinds) != len(want) {
		t.Fatalf("got %d segments, want %d: %+v", len(kinds), len(want), segs)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Errorf("segment %d kind = %d, want %d", i, kinds[i], want[i])
		}
	}
	if segs[1].lang != "go" || segs[3].lang != "json" {
		t.Errorf("fence language tags not captured: %+v", segs)
	}
}

func TestContentAwareMixed(t *testing.T) {
	text := "# Heading\n\nSome prose here.\n\n```go\n// drop me\nfunc f() { x := \"keep // this\" }\n```\n\n```json\n{\n  \"a\": 1\n}\n```"
	out, stats := CompressContentAware(text, fullCompressOpts(0, false, false, nil, 0))
	if stats.Saved <= 0 {
		t.Errorf("expected positive savings; stats=%+v", stats)
	}
	if stats.CodeBlocks != 1 || stats.JSONBlocks != 1 {
		t.Errorf("expected 1 code + 1 json block; stats=%+v", stats)
	}
	if strings.Contains(out, "drop me") {
		t.Errorf("code comment survived; out:\n%s", out)
	}
	if !strings.Contains(out, `"keep // this"`) {
		t.Errorf("code string literal corrupted; out:\n%s", out)
	}
	if !strings.Contains(out, `{"a":1}`) {
		t.Errorf("JSON not compacted; out:\n%s", out)
	}
	// Fences are preserved so the model still sees code blocks.
	if strings.Count(out, "```") != 4 {
		t.Errorf("expected 4 fence markers preserved; out:\n%s", out)
	}
}

func TestContentAwareNeverInflates(t *testing.T) {
	// A tiny string where passes might add overhead must never grow.
	in := "hi"
	out, _ := CompressContentAware(in, fullCompressOpts(0, false, false, nil, 0))
	if len(out) > len(in) {
		t.Errorf("compression inflated input: %q -> %q", in, out)
	}
}

func TestCompressPromptBackCompat(t *testing.T) {
	in := "```json\n{\n  \"x\":  1\n}\n```"
	out, saved := CompressPrompt(in, 0)
	if saved <= 0 {
		t.Errorf("expected savings on pretty JSON; got %d", saved)
	}
	if !strings.Contains(out, `{"x":1}`) {
		t.Errorf("expected compacted JSON; got %q", out)
	}
}

func TestTruncateMiddleBudget(t *testing.T) {
	long := strings.Repeat("alpha beta gamma delta ", 500)
	out := truncateMiddle(long, 50)
	if !strings.Contains(out, "truncated for cost optimisation") {
		t.Error("expected truncation marker")
	}
	if CountTokens(out) > CountTokens(long) {
		t.Error("truncation grew the text")
	}
}

func TestTabularizeJSON(t *testing.T) {
	in := `[{"id":1,"name":"a","active":true},{"id":2,"name":"b","active":false},{"id":3,"name":"c","active":true}]`
	plain, _ := crushJSONOpts(in, false, false)
	tab, ok := crushJSONOpts(in, false, true)
	if !ok {
		t.Fatal("crushJSONOpts ok=false")
	}
	if !strings.Contains(tab, "__cols__") || !strings.Contains(tab, "__rows__") {
		t.Errorf("expected tabular form; got %q", tab)
	}
	// Keys appear once in the column header, not once per row.
	if strings.Count(tab, `"id"`) != 1 || strings.Count(tab, `"name"`) != 1 {
		t.Errorf("keys not hoisted; got %q", tab)
	}
	if len(tab) >= len(plain) {
		t.Errorf("tabular form (%d) should be smaller than plain (%d)", len(tab), len(plain))
	}
}

func TestTabularizeSkipsSmallOrNonUniform(t *testing.T) {
	// Below minTabularRows.
	small := `[{"a":1},{"a":2}]`
	if out, _ := crushJSONOpts(small, false, true); strings.Contains(out, "__cols__") {
		t.Errorf("should not tabularise < %d rows; got %q", minTabularRows, out)
	}
	// Non-uniform key sets.
	mixed := `[{"a":1},{"b":2},{"a":3}]`
	if out, _ := crushJSONOpts(mixed, false, true); strings.Contains(out, "__cols__") {
		t.Errorf("should not tabularise non-uniform objects; got %q", out)
	}
}

func TestCCRStorePutGetDedup(t *testing.T) {
	s := newCCRStore(2)
	id1 := s.Put("hello world")
	id1b := s.Put("hello world")
	if id1 != id1b {
		t.Error("identical content should dedupe to same id")
	}
	got, ok := s.Get(id1)
	if !ok || got != "hello world" {
		t.Errorf("Get returned %q,%v", got, ok)
	}
	// Eviction: capacity 2, add 2 more distinct -> id1 evicted (FIFO).
	s.Put("second")
	s.Put("third")
	if _, ok := s.Get(id1); ok {
		t.Error("expected oldest entry evicted at capacity")
	}
}

func TestCCRElision(t *testing.T) {
	store := newCCRStore(16)
	big := strings.Repeat("data ", 400) // ~2000 chars of prose
	opts := fullCompressOpts(0, false, false, store, 500)
	out, stats := CompressContentAware(big, opts)
	if stats.Elided != 1 {
		t.Fatalf("expected 1 elided segment; stats=%+v", stats)
	}
	if !strings.Contains(out, "headroom-elided id=") {
		t.Errorf("expected elision stub; got %q", out)
	}
	if len(out) >= len(big) {
		t.Errorf("elision did not shrink output: %d -> %d", len(big), len(out))
	}
	// Pull the id out of the stub and round-trip it through the store.
	marker := "headroom-elided id="
	i := strings.Index(out, marker) + len(marker)
	gotID := out[i : i+16]
	orig, ok := store.Get(gotID)
	if !ok {
		t.Fatalf("stored content not found for id %s", gotID)
	}
	if len(orig) == 0 {
		t.Error("retrieved content is empty")
	}
}

func TestCodeWhitespaceCollapse(t *testing.T) {
	src := "```go\n" + `func f() {


	x := 1


	s := "keep this"

	return x
}
` + "```"
	out, _ := CompressContentAware(src, fullCompressOpts(0, false, false, nil, 0))
	// Blank lines between statements collapse away: no consecutive newlines.
	if strings.Contains(out, "\n\n") {
		t.Errorf("expected blank lines collapsed in code; out:\n%s", out)
	}
	if strings.Contains(out, " \n") {
		t.Errorf("expected trailing whitespace trimmed; out:\n%s", out)
	}
	if !strings.Contains(out, "x := 1") || !strings.Contains(out, `"keep this"`) {
		t.Errorf("code content lost; out:\n%s", out)
	}
}

func TestCodeWhitespacePreservesStringBlanks(t *testing.T) {
	// Blank lines INSIDE a raw/backtick string literal must survive untouched,
	// even though blank lines in the surrounding code are dropped.
	src := "```go\nx := 1\n\n\ny := " + "`line one\n\n\nline two`" + "\n\n\nz := 2\n```"
	out, _ := CompressContentAware(src, fullCompressOpts(0, false, false, nil, 0))
	if !strings.Contains(out, "line one\n\n\nline two") {
		t.Errorf("blank lines inside string literal were corrupted; out:\n%s", out)
	}
	// Surrounding code blanks are still collapsed.
	if !strings.Contains(out, "x := 1\ny :=") {
		t.Errorf("code blank lines not collapsed around string; out:\n%s", out)
	}
}

func TestUnionTabularize(t *testing.T) {
	// Sparse records: not all keys present. Union mode requires dropNulls=true.
	in := `[{"id":1,"name":"a","extra":"x"},{"id":2,"name":"b"},{"id":3,"name":"c"}]`
	// Strict mode (dropNulls=false) must NOT tabularise (keys differ).
	if out, _ := crushJSONOpts(in, false, true); strings.Contains(out, "__cols__") {
		t.Errorf("strict mode should not tabularise sparse records; got %q", out)
	}
	// Union mode (dropNulls=true) tabularises, filling the missing cell null.
	out, ok := crushJSONOpts(in, true, true)
	if !ok {
		t.Fatal("crushJSONOpts ok=false")
	}
	if !strings.Contains(out, "__cols__") || !strings.Contains(out, "__rows__") {
		t.Errorf("union mode should tabularise sparse records; got %q", out)
	}
	if !strings.Contains(out, "null") {
		t.Errorf("missing cell should be null-filled; got %q", out)
	}
}

func TestUnionTabularizeRejectsHeterogeneous(t *testing.T) {
	// Wildly different objects: union too large relative to smallest record.
	in := `[{"a":1},{"b":2,"c":3,"d":4,"e":5},{"f":6,"g":7,"h":8}]`
	if out, _ := crushJSONOpts(in, true, true); strings.Contains(out, "__cols__") {
		t.Errorf("should not tabularise heterogeneous records; got %q", out)
	}
}

func TestLosslessProfileKeepsComments(t *testing.T) {
	// The always-on lossless profile must NOT strip code comments.
	text := "```go\n// keep this comment\nx := 1\n```"
	out, _ := CompressContentAware(text, losslessCompressOpts())
	if !strings.Contains(out, "keep this comment") {
		t.Errorf("lossless profile must keep comments; got:\n%s", out)
	}
}

func TestLosslessProfileCompactsJSON(t *testing.T) {
	text := "```json\n{\n  \"a\":  1\n}\n```"
	out, stats := CompressContentAware(text, losslessCompressOpts())
	if !strings.Contains(out, `{"a":1}`) {
		t.Errorf("lossless profile should still compact JSON; got %q", out)
	}
	if stats.Saved <= 0 {
		t.Errorf("expected savings from JSON compaction; stats=%+v", stats)
	}
}
