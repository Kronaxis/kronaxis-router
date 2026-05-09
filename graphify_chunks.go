package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// graphify chunker: produces overlapping content chunks from a file. Roughly
// token-budgeted via char count (4 chars ≈ 1 token).
//
// Strategy:
// - For markdown: split on headings first, then char-window each section.
// - For code (ext .go .py .ts .js .rs .java): split on top-level decls (func, class, def).
// - Otherwise: pure char-window with overlap.
//
// All chunks include a leading "[file: path]\n" tag so the LLM has provenance.

const (
	chunkTargetTokens = 800
	chunkOverlapTokens = 150
	avgCharsPerToken  = 4
)

type Chunk struct {
	SourcePath string
	Idx        int
	Content    string
	Metadata   map[string]any
}

func chunkFile(path string) ([]Chunk, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, nil
	}
	// Skip binary-ish or huge files
	if info.Size() > 4*1024*1024 {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if !looksLikeText(data) {
		return nil, nil
	}
	ext := strings.ToLower(filepath.Ext(path))
	body := string(data)

	switch ext {
	case ".md", ".mdx":
		return chunkMarkdown(path, body), nil
	case ".go", ".py", ".ts", ".tsx", ".js", ".jsx", ".rs", ".java", ".c", ".h", ".cc", ".cpp", ".rb", ".php":
		return chunkCode(path, body), nil
	default:
		return chunkCharWindow(path, body), nil
	}
}

func looksLikeText(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	// Reject if any NUL byte (binary marker). Scan the whole buffer because a
	// file can have a clean UTF-8 prefix and a NUL further in (e.g. zip with
	// a leading text comment).
	for _, b := range data {
		if b == 0 {
			return false
		}
	}
	// Reject if any byte sequence isn't valid UTF-8. Postgres rejects non-UTF-8
	// content at upsert time, so anything we'd insert that fails this check
	// would crash the ingest worker. utf8.Valid is fast (~1 GB/s) and the
	// chunker already caps at 4 MiB per file via chunkFile.
	if !utf8.Valid(data) {
		return false
	}
	return true
}

func chunkMarkdown(path, body string) []Chunk {
	sections := splitMarkdownByHeadings(body)
	var chunks []Chunk
	idx := 0
	for _, sec := range sections {
		for _, c := range chunkCharWindow(path, sec) {
			c.Idx = idx
			chunks = append(chunks, c)
			idx++
		}
	}
	return chunks
}

func splitMarkdownByHeadings(body string) []string {
	scanner := bufio.NewScanner(strings.NewReader(body))
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var sections []string
	var current strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "# ") || strings.HasPrefix(line, "## ") || strings.HasPrefix(line, "### ") {
			if current.Len() > 0 {
				sections = append(sections, current.String())
				current.Reset()
			}
		}
		current.WriteString(line)
		current.WriteString("\n")
	}
	if current.Len() > 0 {
		sections = append(sections, current.String())
	}
	return sections
}

// chunkCode splits at top-level Go/Python/TS-ish definitions.
func chunkCode(path, body string) []Chunk {
	lines := strings.Split(body, "\n")
	var sections []string
	var cur strings.Builder
	for _, ln := range lines {
		if isTopLevelDecl(ln) && cur.Len() > 0 {
			sections = append(sections, cur.String())
			cur.Reset()
		}
		cur.WriteString(ln)
		cur.WriteString("\n")
	}
	if cur.Len() > 0 {
		sections = append(sections, cur.String())
	}
	var chunks []Chunk
	idx := 0
	for _, sec := range sections {
		for _, c := range chunkCharWindow(path, sec) {
			c.Idx = idx
			chunks = append(chunks, c)
			idx++
		}
	}
	return chunks
}

func isTopLevelDecl(line string) bool {
	trimmed := strings.TrimLeft(line, " \t")
	if trimmed != line {
		// indented -- not top-level
		return false
	}
	switch {
	case strings.HasPrefix(trimmed, "func "):
		return true
	case strings.HasPrefix(trimmed, "type "):
		return true
	case strings.HasPrefix(trimmed, "def "):
		return true
	case strings.HasPrefix(trimmed, "class "):
		return true
	case strings.HasPrefix(trimmed, "export function "), strings.HasPrefix(trimmed, "export class "):
		return true
	case strings.HasPrefix(trimmed, "fn "), strings.HasPrefix(trimmed, "pub fn "):
		return true
	}
	return false
}

func chunkCharWindow(path, body string) []Chunk {
	target := chunkTargetTokens * avgCharsPerToken
	overlap := chunkOverlapTokens * avgCharsPerToken
	if len(body) == 0 {
		return nil
	}
	if len(body) <= target {
		return []Chunk{{
			SourcePath: path,
			Idx:        0,
			Content:    formatChunk(path, body),
			Metadata:   map[string]any{"length_chars": len(body)},
		}}
	}
	var chunks []Chunk
	idx := 0
	for start := 0; start < len(body); {
		end := start + target
		if end > len(body) {
			end = len(body)
		}
		// Try to break on a newline near the end
		if end < len(body) {
			if nl := strings.LastIndex(body[start:end], "\n"); nl > target/2 {
				end = start + nl
			}
		}
		// Snap end to a UTF-8 rune boundary so we never cut a multibyte
		// codepoint in half. Continuation bytes have high bits 10xxxxxx;
		// back up until we hit a lead byte (or ASCII).
		end = snapRuneBoundary(body, end)
		chunks = append(chunks, Chunk{
			SourcePath: path,
			Idx:        idx,
			Content:    formatChunk(path, body[start:end]),
			Metadata:   map[string]any{"length_chars": end - start},
		})
		idx++
		if end >= len(body) {
			break
		}
		start = snapRuneBoundary(body, end-overlap)
		if start < 0 {
			start = 0
		}
	}
	return chunks
}

// snapRuneBoundary returns the largest index <= i that is at the start of a
// UTF-8 rune (or len(s) if i is at end). Used by the chunker to avoid
// splitting a multibyte codepoint, which would produce invalid UTF-8 that
// Postgres rejects on upsert.
func snapRuneBoundary(s string, i int) int {
	if i <= 0 {
		return 0
	}
	if i >= len(s) {
		return len(s)
	}
	// Walk back over continuation bytes (10xxxxxx).
	for i > 0 && (s[i]&0xC0) == 0x80 {
		i--
	}
	return i
}

func formatChunk(path, body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	return "[file: " + path + "]\n" + body
}
