package ignores

import (
	"strings"
	"unicode"
)

// scriptTools returns the tools that a script of the root package.json runs.
// The tasks that follow reuse it, so it keys the result by tool name.
func scriptTools(pkg *packageJSON) map[string]bool {
	found := map[string]bool{}
	if pkg == nil {
		return found
	}
	for _, script := range pkg.Scripts {
		for _, words := range splitCommands(script) {
			for i, w := range words {
				switch w {
				case "vitest", "jest", "prettier", "eslint", "biome":
					found[w] = true
				case "playwright":
					if i+1 < len(words) && words[i+1] == "test" {
						found[w] = true
					}
				}
			}
		}
	}
	return found
}

// splitCommands splits a script on `&&`, `||` and `;` into commands, and each
// command into words. A quoted argument becomes an empty word, so it never
// matches a tool name.
func splitCommands(script string) [][]string {
	var commands [][]string
	var words []string
	var word strings.Builder
	flushWord := func() {
		if word.Len() > 0 {
			words = append(words, word.String())
			word.Reset()
		}
	}
	flushCommand := func() {
		flushWord()
		if len(words) > 0 {
			commands = append(commands, words)
			words = nil
		}
	}
	for i := 0; i < len(script); i++ {
		c := script[i]
		switch {
		case c == '\'' || c == '"':
			flushWord()
			words = append(words, "")
			end := strings.IndexByte(script[i+1:], c)
			if end < 0 {
				i = len(script)
			} else {
				i += end + 1
			}
		case c == ';':
			flushCommand()
		case (c == '&' || c == '|') && i+1 < len(script) && script[i+1] == c:
			flushCommand()
			i++
		case unicode.IsSpace(rune(c)):
			flushWord()
		default:
			word.WriteByte(c)
		}
	}
	flushCommand()
	return commands
}
