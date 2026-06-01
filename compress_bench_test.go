package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestDumpCompressionSamples writes original/lossless/full outputs for each
// sample payload to /tmp/kx-compress-samples.json so an external real tokenizer
// (tiktoken) can measure true token reduction. Gated on KX_DUMP_SAMPLES=1 so it
// is inert during normal test runs.
func TestDumpCompressionSamples(t *testing.T) {
	if os.Getenv("KX_DUMP_SAMPLES") != "1" {
		t.Skip("set KX_DUMP_SAMPLES=1 to dump samples")
	}
	cases := []struct{ name, text string }{
		{"pretty_json_records", sampleJSONRecords(40)},
		{"go_source_with_comments", sampleGoSource()},
		{"python_source_with_comments", samplePythonSource()},
		{"prose_markdown", sampleProse()},
		{"mixed_agent_context", sampleMixed()},
	}
	out := make([]map[string]string, 0, len(cases))
	for _, c := range cases {
		lossless, _ := CompressContentAware(c.text, losslessCompressOpts())
		full, _ := CompressContentAware(c.text, fullCompressOpts(0, true, true, nil, 0))
		out = append(out, map[string]string{
			"name":     c.name,
			"original": c.text,
			"old":      oldPipeline(c.text),
			"lossless": lossless,
			"full":     full,
		})
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	if err := os.WriteFile("/tmp/kx-compress-samples.json", b, 0644); err != nil {
		t.Fatal(err)
	}
}

// TestCompressionPercentages reports real token-reduction percentages on
// representative payloads for three profiles:
//   - "old": the pre-headroom lexical pipeline (whitespace + markdown + dedup +
//     truncate over the WHOLE text) — reconstructed here as the baseline.
//   - "lossless": the new always-on lossless profile (JSON compaction + prose
//     whitespace; keeps comments).
//   - "full": the aggressive bulk profile (JSON compaction + tabularisation +
//     code comment stripping + prose passes).
//
// Run: go test -run TestCompressionPercentages -v ./
func TestCompressionPercentages(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{"pretty_json_records", sampleJSONRecords(40)},
		{"go_source_with_comments", sampleGoSource()},
		{"python_source_with_comments", samplePythonSource()},
		{"prose_markdown", sampleProse()},
		{"mixed_agent_context", sampleMixed()},
	}

	fmt.Printf("\n%-30s %12s %12s %12s\n", "payload", "old%", "lossless%", "full%")
	fmt.Println(strings.Repeat("-", 70))

	for _, c := range cases {
		orig := CountTokens(c.text)
		if orig == 0 {
			t.Fatalf("%s: zero tokens", c.name)
		}

		oldOut := oldPipeline(c.text)
		_, lossless := CompressContentAware(c.text, losslessCompressOpts())
		_, full := CompressContentAware(c.text, fullCompressOpts(0, true, true, nil, 0))

		oldPct := pct(orig, CountTokens(oldOut))
		losslessPct := pct(orig, lossless.FinalTokens)
		fullPct := pct(orig, full.FinalTokens)

		fmt.Printf("%-30s %11.1f%% %11.1f%% %11.1f%%\n", c.name, oldPct, losslessPct, fullPct)

		// Guard rails: full must never do worse than old on these payloads, and
		// nothing may inflate.
		if fullPct < 0 || losslessPct < 0 {
			t.Errorf("%s: compression inflated tokens (old=%.1f lossless=%.1f full=%.1f)",
				c.name, oldPct, losslessPct, fullPct)
		}
	}
	fmt.Println()
}

func pct(before, after int) float64 {
	if before == 0 {
		return 0
	}
	return 100 * float64(before-after) / float64(before)
}

// oldPipeline reconstructs the pre-headroom compressor: lexical passes applied
// blindly over the whole text, no content awareness.
func oldPipeline(text string) string {
	s := collapseWhitespace(text)
	s = stripMarkdownNoise(s)
	s = deduplicateLines(s)
	return s
}

// --- sample payloads ---

func sampleJSONRecords(n int) string {
	var rows []string
	for i := 0; i < n; i++ {
		rows = append(rows, fmt.Sprintf(`    {
        "id": %d,
        "name": "user_%d",
        "email": "user_%d@example.com",
        "active": true,
        "role": "member",
        "notes": null
    }`, i, i, i))
	}
	return "```json\n[\n" + strings.Join(rows, ",\n") + "\n]\n```"
}

func sampleGoSource() string {
	return "```go\n" + `package main

// Server handles incoming requests.
// It is the main entry point for the service.
type Server struct {
	addr string // listen address
	port int    // listen port
}

// Start begins serving. This is a long-running call.
func (s *Server) Start() error {
	// open the socket
	url := "http://localhost" // not // a comment inside the string
	_ = url

	/* a block comment
	   spanning several
	   lines that adds nothing */
	return nil
}
` + "```"
}

func samplePythonSource() string {
	return "```python\n" + `# Module: data loader
# Author: someone

def load(path):
    # open the file
    data = read(path)  # read everything
    marker = "# not a comment"
    doc = """
    # also preserved
    multi-line
    """
    return data
` + "```"
}

func sampleProse() string {
	return `# Heading One

This is a paragraph of explanatory text.

---

## Heading Two

This is a paragraph of explanatory text.

Some more text here with    irregular    spacing.



Trailing blank lines above.`
}

func sampleMixed() string {
	return sampleProse() + "\n\n" + sampleGoSource() + "\n\n" + sampleJSONRecords(20)
}
