// Package loop is `batuta loop`: the mechanical conductor on a file host.
// It drives routing.DeliveryGraph over an approved plan — routing from the
// user's table, one executor session per task in its own worktree through
// the adapter, the four gates, retry then escalation, canonical integration
// onto the user's branch — and journals every transition so `--resume`
// continues from the last recorded operation.
package loop

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Profile is the machine-readable part of `.batuta/profile.md`: the
// literal lines batuta-init writes. Everything else in the file is prose
// the brief carries as conventions.
type Profile struct {
	Stack       string
	Methodology string
	Test        string
	Build       string
	Install     string
	Execution   string // sequential | parallel
	Worktree    string // off | medium+ | always (the loop always uses worktrees)
	Template    string // templates/<stack>.md
	Raw         string
}

var profileLine = regexp.MustCompile(`^(Stack|Methodology|Test|Build|Install|Execution|Worktree|Template):\s*(.*)$`)

// LoadProfile reads `.batuta/profile.md` under the workspace root.
func LoadProfile(root string) (Profile, error) {
	payload, err := os.ReadFile(filepath.Join(root, ".batuta", "profile.md"))
	if err != nil {
		return Profile{}, errors.New("loop: .batuta/profile.md is missing — run /batuta-init first")
	}
	return ParseProfile(string(payload)), nil
}

// ParseProfile extracts the literal lines; the first occurrence wins.
func ParseProfile(payload string) Profile {
	profile := Profile{Raw: payload}
	seen := map[string]bool{}
	scanner := bufio.NewScanner(strings.NewReader(payload))
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		match := profileLine.FindStringSubmatch(strings.TrimRight(scanner.Text(), " \t"))
		if match == nil || seen[match[1]] {
			continue
		}
		seen[match[1]] = true
		value := strings.TrimSpace(match[2])
		switch match[1] {
		case "Stack":
			profile.Stack = value
		case "Methodology":
			profile.Methodology = value
		case "Test":
			profile.Test = value
		case "Build":
			profile.Build = value
		case "Install":
			profile.Install = value
		case "Execution":
			profile.Execution = strings.ToLower(value)
		case "Worktree":
			profile.Worktree = strings.ToLower(value)
		case "Template":
			profile.Template = strings.Trim(value, "`")
		}
	}
	return profile
}

// Parallelism is how many executors a wave may run at once.
func (p Profile) Parallelism() int {
	if p.Execution == "parallel" {
		return 4
	}
	return 1
}

// FindSkills locates the installed `batuta` skill directory (the one with
// `adapters/` and `templates/`): an explicit path, then the workspace, then
// the shared and plugin locations on this machine. The newest plugin cache
// version wins when several are installed.
func FindSkills(root, explicit string) (string, error) {
	candidates := []string{}
	if explicit != "" {
		candidates = append(candidates, explicit)
	}
	if env := strings.TrimSpace(os.Getenv("BATUTA_SKILLS")); env != "" {
		candidates = append(candidates, env)
	}
	candidates = append(candidates, filepath.Join(root, ".agents", "skills", "batuta"), filepath.Join(root, ".claude", "skills", "batuta"))
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidates = append(candidates, filepath.Join(home, ".agents", "skills", "batuta"))
		for _, host := range []string{".claude", ".codex"} {
			candidates = append(candidates, newestPluginSkills(filepath.Join(home, host, "plugins", "cache", "batuta", "batuta"))...)
		}
		candidates = append(candidates,
			filepath.Join(home, ".claude", "skills", "batuta"),
			filepath.Join(home, ".codex", "skills", "batuta"),
			filepath.Join(home, ".config", "opencode", "skills", "batuta"),
		)
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(candidate, "adapters")); err == nil {
			if resolved, err := filepath.EvalSymlinks(candidate); err == nil {
				return resolved, nil
			}
			return candidate, nil
		}
	}
	return "", errors.New("loop: the batuta skills are not installed (no adapters/ directory found); pass --skills <dir> or run the installer")
}

func newestPluginSkills(cache string) []string {
	entries, err := os.ReadDir(cache)
	if err != nil {
		return nil
	}
	versions := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			versions = append(versions, entry.Name())
		}
	}
	sort.Slice(versions, func(i, j int) bool { return versionLess(versions[j], versions[i]) })
	paths := make([]string, 0, len(versions))
	for _, version := range versions {
		paths = append(paths, filepath.Join(cache, version, "skills", "batuta"))
	}
	return paths
}

// versionLess orders dotted numeric versions; anything else sorts by string.
func versionLess(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for index := 0; index < len(as) && index < len(bs); index++ {
		var ai, bi int
		if _, err := fmt.Sscanf(as[index], "%d", &ai); err != nil {
			return a < b
		}
		if _, err := fmt.Sscanf(bs[index], "%d", &bi); err != nil {
			return a < b
		}
		if ai != bi {
			return ai < bi
		}
	}
	return len(as) < len(bs)
}

var templateExtends = regexp.MustCompile("Extends\\s+`?templates/([a-z0-9-]+)\\.md`?")

// Conventions returns the "Conventions for briefs" sections of the
// profile's template and of every template in its Extends chain, child
// first up to generic.md. Missing templates are skipped: the brief then
// says so instead of failing the run.
func Conventions(skillsRoot, template string) (sections []string, missing []string) {
	name := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(template), "templates/"), ".md")
	if name == "" {
		name = "generic"
	}
	seen := map[string]bool{}
	for name != "" && !seen[name] {
		seen[name] = true
		payload, err := os.ReadFile(filepath.Join(skillsRoot, "templates", name+".md"))
		if err != nil {
			missing = append(missing, name)
			break
		}
		text := string(payload)
		if section := sectionOf(text, "## Conventions for briefs"); section != "" {
			sections = append(sections, "### From templates/"+name+".md\n\n"+section)
		}
		next := ""
		if match := templateExtends.FindStringSubmatch(text); match != nil {
			next = match[1]
		} else if name != "generic" {
			next = "generic"
		}
		name = next
	}
	return sections, missing
}

// sectionOf returns the body of a `## heading` up to the next `## `.
func sectionOf(text, heading string) string {
	start := strings.Index(text, heading)
	if start < 0 {
		return ""
	}
	body := text[start+len(heading):]
	if end := strings.Index(body, "\n## "); end >= 0 {
		body = body[:end]
	}
	return strings.TrimSpace(body)
}
