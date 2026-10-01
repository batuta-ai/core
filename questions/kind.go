package questions

import (
	"path"
	"strings"
)

var environmentWords = []string{
	"sandbox", "docker", "colima", "socket", "not permitted", "permission",
	"gocache", "network", "loopback", "/bin/ps", "write access", "connectivity",
	"container", "disk", "disco", "ambiente", "environment", "toolchain",
}

var scopeWords = []string{"scope", "escopo", "install", "instalar", "dependenc"}

var continueWords = []string{
	"stop condition", "continue past", "may i continue", "may i resume",
	"posso continuar", "posso retomar", "pode retomar", "nova tentativa",
	"retry", "resume this", "retomar est", "resume verification", "continue verification",
}

var pathExtensions = map[string]bool{
	".go": true, ".ts": true, ".tsx": true, ".js": true, ".php": true,
	".md": true, ".json": true, ".yaml": true, ".yml": true,
	".lock": true, ".sh": true,
}

func containsWord(text string, words []string) bool {
	for _, word := range words {
		if strings.Contains(text, word) {
			return true
		}
	}
	return false
}

func namesPathOutsideScope(text string, scope []string) bool {
	for _, raw := range strings.Fields(text) {
		token := strings.Trim(raw, "`'\"()[]{}<>,;:?!")
		if !strings.Contains(token, "/") || !pathExtensions[path.Ext(token)] {
			continue
		}
		inside := false
		for _, entry := range scope {
			entry = strings.ToLower(strings.TrimSpace(entry))
			matched, _ := path.Match(entry, token)
			if token == entry || strings.HasPrefix(token, strings.TrimSuffix(entry, "/")+"/") || matched {
				inside = true
				break
			}
		}
		if !inside {
			return true
		}
	}
	return false
}

// Kind applies section 15's ordered code rules to an executor question.
func Kind(text string, scope []string) string {
	text = strings.ToLower(text)
	switch {
	case containsWord(text, environmentWords):
		return "environment"
	case containsWord(text, scopeWords) || namesPathOutsideScope(text, scope):
		return "scope_change"
	case containsWord(text, continueWords):
		return "continue"
	default:
		return "other"
	}
}
