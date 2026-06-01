package main

import (
	"strings"
	"testing"
)

func TestChunkCharWindow_Small(t *testing.T) {
	body := "hello kronaxis world"
	chunks := chunkCharWindow("/x/y.txt", body)
	if len(chunks) != 1 {
		t.Fatalf("len=%d want 1", len(chunks))
	}
	if !strings.Contains(chunks[0].Content, "[file: /x/y.txt]") {
		t.Errorf("chunk missing file tag: %q", chunks[0].Content)
	}
	if !strings.Contains(chunks[0].Content, "kronaxis") {
		t.Errorf("chunk missing body: %q", chunks[0].Content)
	}
}

func TestChunkCharWindow_Overlap(t *testing.T) {
	body := strings.Repeat("a b c d e f g h i j k l m n o p\n", 1000) // ~32 KiB
	chunks := chunkCharWindow("/x/big.txt", body)
	if len(chunks) < 8 {
		t.Errorf("expected many chunks, got %d", len(chunks))
	}
	// All chunks must include the file tag
	for i, c := range chunks {
		if !strings.HasPrefix(c.Content, "[file: /x/big.txt]") {
			t.Errorf("chunk %d missing file tag", i)
		}
	}
	// chunk_idx must be monotonic from 0
	for i, c := range chunks {
		if c.Idx != i {
			t.Errorf("chunk %d has Idx=%d", i, c.Idx)
		}
	}
}

func TestSplitMarkdownByHeadings(t *testing.T) {
	doc := `# A
para a1

## B
para b1

# C
para c1
`
	sections := splitMarkdownByHeadings(doc)
	if len(sections) != 3 {
		t.Fatalf("len=%d want 3", len(sections))
	}
	if !strings.HasPrefix(sections[0], "# A") {
		t.Errorf("section 0 wrong: %q", sections[0])
	}
	if !strings.HasPrefix(sections[1], "## B") {
		t.Errorf("section 1 wrong: %q", sections[1])
	}
	if !strings.HasPrefix(sections[2], "# C") {
		t.Errorf("section 2 wrong: %q", sections[2])
	}
}

func TestIsTopLevelDecl(t *testing.T) {
	cases := map[string]bool{
		"func Foo() {":               true,
		"func (x *Foo) Bar() {":      true,
		"  func indented() {":        false,
		"type Bar struct {":          true,
		"def something():":           true,
		"class Foo:":                 true,
		"export function tsFunc() {": true,
		"// just a comment":          false,
		"":                           false,
	}
	for in, want := range cases {
		if got := isTopLevelDecl(in); got != want {
			t.Errorf("isTopLevelDecl(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestVectorLiteral(t *testing.T) {
	v := []float32{0.1, -0.2, 0.5}
	got := vectorLiteral(v)
	want := "[0.1,-0.2,0.5]"
	if got != want {
		t.Errorf("vectorLiteral = %q, want %q", got, want)
	}
}

func TestSnapRuneBoundary(t *testing.T) {
	// "café" = 0x63 'c', 0x61 'a', 0x66 'f', 0xC3 0xA9 (é = 2 bytes)
	s := "café"
	if got := snapRuneBoundary(s, 4); got != 3 {
		// Index 4 is mid-rune (between 0xC3 and 0xA9). Should snap to 3.
		t.Errorf("snap mid-multibyte: got %d, want 3", got)
	}
	if got := snapRuneBoundary(s, 5); got != 5 {
		t.Errorf("snap at end: got %d, want 5", got)
	}
	if got := snapRuneBoundary(s, 0); got != 0 {
		t.Errorf("snap at zero: got %d, want 0", got)
	}
	if got := snapRuneBoundary(s, -1); got != 0 {
		t.Errorf("snap negative: got %d, want 0", got)
	}
	if got := snapRuneBoundary(s, 100); got != len(s) {
		t.Errorf("snap past end: got %d, want %d", got, len(s))
	}
}

func TestChunkCharWindow_ValidUTF8AcrossBoundaries(t *testing.T) {
	// Build a body big enough to chunk, with multibyte chars at every
	// possible offset so any naive split would slice mid-codepoint.
	var b strings.Builder
	for i := 0; i < 2000; i++ {
		b.WriteString("é") // 2-byte UTF-8 (0xC3 0xA9)
	}
	body := b.String()

	chunks := chunkCharWindow("/x/multibyte.txt", body)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if !utf8Valid([]byte(c.Content)) {
			t.Errorf("chunk %d contains invalid UTF-8: %q...", i, c.Content[:50])
		}
	}
}

// utf8Valid is a tiny adapter to avoid pulling unicode/utf8 into the test file.
func utf8Valid(b []byte) bool { return looksLikeText(b) }

func TestEffectiveMinCosineSim(t *testing.T) {
	// Unset (nil) → default 0.4
	g := GraphifyConfig{}
	if got := g.EffectiveMinCosineSim(); got != 0.4 {
		t.Errorf("nil → got %v, want 0.4", got)
	}
	// Explicit 0.0 → no filter (the bug that triggered this fix)
	zero := 0.0
	g = GraphifyConfig{MinCosineSim: &zero}
	if got := g.EffectiveMinCosineSim(); got != 0.0 {
		t.Errorf("0.0 → got %v, want 0.0", got)
	}
	// Explicit positive → honoured
	v := 0.7
	g = GraphifyConfig{MinCosineSim: &v}
	if got := g.EffectiveMinCosineSim(); got != 0.7 {
		t.Errorf("0.7 → got %v, want 0.7", got)
	}
}

func TestLooksLikeText(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want bool
	}{
		{"ascii", []byte("hello world"), true},
		{"valid utf8 british", []byte("hello — kronaxis"), true},
		{"valid utf8 emoji", []byte("kronaxis 🚀"), true},
		{"valid utf8 mixed scripts", []byte("kronaxis 北京 العربية"), true},
		{"nul early", []byte{0x68, 0x00, 0x65}, false},
		{"nul late (whole-buffer scan)", append([]byte("hello "), 0x00), false},
		{"empty", []byte(""), false},

		// Stray 0x80 (continuation byte without lead) -- the exact case that
		// crashed ingest at the Postgres upsert.
		{"stray 0x80", []byte{0x68, 0x80, 0x65}, false},

		// Truncated multibyte sequence (lead byte without continuation).
		{"truncated multibyte", []byte{0x68, 0xC3}, false},

		// Random binary that happens to have no NULs and no valid UTF-8.
		{"binary no nul", []byte{0xFF, 0xFE, 0xFD, 0xFC}, false},
	}
	for _, c := range cases {
		got := looksLikeText(c.in)
		if got != c.want {
			t.Errorf("looksLikeText(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestShouldIngestFile(t *testing.T) {
	cases := map[string]bool{
		"foo.go":   true,
		"foo.md":   true,
		"foo.png":  false,
		".env":     false,
		"foo.exe":  false,
		"foo.yaml": true,
		"makefile": false, // we only allow extension-less for README/LICENSE
	}
	for path, want := range cases {
		if got := shouldIngestFile(path); got != want {
			t.Errorf("shouldIngestFile(%q) = %v, want %v", path, got, want)
		}
	}
}
