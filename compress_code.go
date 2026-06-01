package main

import "strings"

// Code compression for fenced code blocks. Clean-room Go reimplementation of the
// idea behind headroom's "CodeCompressor" (https://github.com/chopratejas/headroom,
// Apache-2.0): comments and comment-only lines are the tokens a model rarely
// needs, so drop them while never touching anything that changes behaviour.
//
// This is a *string-literal-aware lexical scanner*, NOT a full AST parse. It
// tracks string / char / template-literal state, so it will never strip a "//"
// or "#" that lives inside a string. Comment stripping is enabled only for
// languages where the simple model is provably safe. Notably EXCLUDED:
//   - shell/bash: `#` is not always a comment (`${var#prefix}`, heredocs)
//   - yaml/toml/ini: low comment density, heredoc/anchor edge cases
// For those (and unknown languages) compressCode returns the input unchanged.
// No headroom source was copied; see NOTICE.

type commentStyle struct {
	lineComments []string // e.g. {"//"} or {"#"} or {"--"}
	blockOpen    string   // "/*" or ""
	blockClose   string   // "*/" or ""
	stringDelims string   // each byte is a string delimiter, e.g. "\"'`"
	tripleQuote  bool     // python-style triple-quoted strings ("""/''')
}

// styleForLang returns the comment style for a fenced-block language tag, and
// whether comment stripping is safe for it. ok=false → caller leaves code as-is.
func styleForLang(lang string) (commentStyle, bool) {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "go", "golang", "c", "cc", "cpp", "c++", "h", "hpp", "hxx", "java",
		"js", "javascript", "jsx", "ts", "typescript", "tsx", "rust", "rs",
		"kotlin", "kt", "swift", "scala", "cs", "csharp", "dart":
		return commentStyle{
			lineComments: []string{"//"},
			blockOpen:    "/*",
			blockClose:   "*/",
			stringDelims: "\"'`",
		}, true
	case "python", "py":
		return commentStyle{
			lineComments: []string{"#"},
			stringDelims: "\"'",
			tripleQuote:  true,
		}, true
	case "sql":
		return commentStyle{
			lineComments: []string{"--"},
			blockOpen:    "/*",
			blockClose:   "*/",
			stringDelims: "'\"",
		}, true
	default:
		// Includes shell, bash, yaml, toml, ini, ruby, and unknown tags: not
		// safe to strip comments, so signal "leave as-is".
		return commentStyle{}, false
	}
}

// compressCode strips comments (where safe for the language) from a fenced code
// block body. Code that is not in the safe-language set is returned unchanged.
func compressCode(code, lang string) string {
	style, ok := styleForLang(lang)
	if !ok {
		return code
	}
	return stripComments(code, style)
}

// stripComments removes line and block comments while respecting string
// literals. Comment-only lines (whitespace + comment) are removed entirely,
// including their trailing newline; trailing comments are removed but the code
// line and its newline are kept (with the gap before the comment trimmed).
func stripComments(src string, st commentStyle) string {
	b := []byte(src)
	n := len(b)
	out := make([]byte, 0, n)
	i := 0
	for i < n {
		c := b[i]

		// String literal?
		if strings.IndexByte(st.stringDelims, c) >= 0 {
			// Triple-quoted (python) multiline string.
			if st.tripleQuote && i+2 < n && b[i+1] == c && b[i+2] == c {
				out = append(out, c, c, c)
				i += 3
				for i < n {
					if b[i] == '\\' && i+1 < n {
						out = append(out, b[i], b[i+1])
						i += 2
						continue
					}
					if b[i] == c && i+2 < n && b[i+1] == c && b[i+2] == c {
						out = append(out, c, c, c)
						i += 3
						break
					}
					out = append(out, b[i])
					i++
				}
				continue
			}
			// Ordinary string until matching, unescaped delimiter. Backtick
			// (Go raw / JS template) may span newlines; ordinary quotes stop at
			// a newline defensively to avoid runaway on malformed input.
			out = append(out, c)
			i++
			for i < n {
				if b[i] == '\\' && i+1 < n {
					out = append(out, b[i], b[i+1])
					i += 2
					continue
				}
				ch := b[i]
				out = append(out, ch)
				i++
				if ch == c {
					break
				}
				if ch == '\n' && c != '`' {
					break
				}
			}
			continue
		}

		// Line comment?
		if lc, ok := matchLineComment(b, i, st.lineComments); ok {
			// Is the current output line blank so far (comment-only line)?
			lineStart := len(out)
			for lineStart > 0 && out[lineStart-1] != '\n' {
				lineStart--
			}
			commentOnly := strings.TrimSpace(string(out[lineStart:])) == ""
			// Advance past the comment to end of line.
			for i < n && b[i] != '\n' {
				i++
			}
			if commentOnly {
				// Drop the line's leading whitespace and the newline too, so the
				// whole line vanishes.
				out = out[:lineStart]
				if i < n {
					i++ // consume the newline
				}
			} else {
				// Trailing comment: trim the gap we already emitted before it,
				// keep the newline (handled by the next iteration).
				k := len(out)
				for k > 0 && (out[k-1] == ' ' || out[k-1] == '\t') {
					k--
				}
				out = out[:k]
			}
			_ = lc
			continue
		}

		// Block comment?
		if st.blockOpen != "" && hasPrefixAt(b, i, st.blockOpen) {
			i += len(st.blockOpen)
			for i < n && !hasPrefixAt(b, i, st.blockClose) {
				i++
			}
			i += len(st.blockClose)
			if i > n {
				i = n
			}
			continue
		}

		// Newline in normal (non-string, non-comment) state: trim the trailing
		// whitespace of the line just finished and drop the line entirely if it
		// is blank. This is reached only outside strings — newlines inside
		// backtick/template/triple-quoted strings are emitted by the string
		// branch above, so string contents are never collapsed. Lossless:
		// blank lines and trailing whitespace never affect code behaviour.
		if c == '\n' {
			k := len(out)
			for k > 0 && (out[k-1] == ' ' || out[k-1] == '\t') {
				k--
			}
			lineStart := k
			for lineStart > 0 && out[lineStart-1] != '\n' {
				lineStart--
			}
			out = out[:k]
			if lineStart != k {
				out = append(out, '\n') // non-blank line: keep its terminator
			}
			i++
			continue
		}

		out = append(out, c)
		i++
	}
	return string(out)
}

func matchLineComment(b []byte, i int, prefixes []string) (string, bool) {
	for _, p := range prefixes {
		if hasPrefixAt(b, i, p) {
			return p, true
		}
	}
	return "", false
}

func hasPrefixAt(b []byte, i int, p string) bool {
	if p == "" || i+len(p) > len(b) {
		return false
	}
	for j := 0; j < len(p); j++ {
		if b[i+j] != p[j] {
			return false
		}
	}
	return true
}
