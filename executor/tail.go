package executor

import (
	"path/filepath"
	"regexp"
	"strings"
)

var (
	secretLine   = regexp.MustCompile(`^[A-Z][A-Z0-9_]*=`)
	absolutePath = regexp.MustCompile(`(?:[A-Za-z]:)?(?:/|\\)[^\s"'=]+`)
)

// IsSecretLine reports whether a line is a secret-shaped environment
// assignment such as KEY=value.
func IsSecretLine(line string) bool {
	return secretLine.MatchString(strings.TrimSpace(line))
}

// DropSecretLines removes every secret-shaped environment assignment line.
func DropSecretLines(value string) string {
	if value == "" {
		return ""
	}
	lines := strings.Split(value, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if IsSecretLine(line) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// RedactPaths strips the workspace prefix and reduces every remaining
// absolute path to its base name. The workspace is stripped only where a
// separator, a quote, whitespace, the end of the value or one of the
// punctuation bytes trimmed from a path end (".,;:)") follows it. A match
// preceded by '.', '\\' or an alphanumeric byte is part of a relative path
// and stays as written.
func RedactPaths(value, workspace string) string {
	if value == "" {
		return ""
	}
	value = stripWorkspace(value, workspace)
	matches := absolutePath.FindAllStringIndex(value, -1)
	if len(matches) == 0 {
		return value
	}
	var b strings.Builder
	last := 0
	for _, loc := range matches {
		start, end := loc[0], loc[1]
		if start > 0 {
			prev := value[start-1]
			if prev == '.' || prev == '\\' || alphanumeric(prev) {
				continue
			}
		}
		match := value[start:end]
		cleaned := strings.TrimRight(match, ".,;:)")
		if strings.HasPrefix(cleaned, "//") {
			continue
		}
		slash := filepath.ToSlash(cleaned)
		if !filepath.IsAbs(cleaned) && !strings.HasPrefix(slash, "/") {
			continue
		}
		b.WriteString(value[last:start])
		b.WriteString(filepath.ToSlash(filepath.Base(cleaned)))
		b.WriteString(match[len(cleaned):])
		last = end
	}
	b.WriteString(value[last:])
	return b.String()
}

func stripWorkspace(value, workspace string) string {
	workspace = strings.TrimRight(workspace, `/\`)
	if workspace == "" {
		return value
	}
	var b strings.Builder
	rest := value
	for {
		index := strings.Index(rest, workspace)
		if index < 0 {
			break
		}
		b.WriteString(rest[:index])
		rest = rest[index+len(workspace):]
		switch {
		case rest == "":
		case rest[0] == '/' || rest[0] == '\\':
			rest = rest[1:]
		case strings.IndexByte("\"'` \t\r\n.,;:)", rest[0]) >= 0:
		default:
			b.WriteString(workspace)
		}
	}
	b.WriteString(rest)
	return b.String()
}

func alphanumeric(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}
