package routing

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
)

const (
	GrantUntilTask = "task"

	maxGrantPaths      = 32
	maxGrantPathBytes  = 256
	maxGrantOperations = 16
)

// GrantBoundaryPaths lists the paths a grant never covers, whatever its globs
// say: the brief's Boundaries. `**/` prefixes mean any depth; the rest anchor at
// the repository root.
var GrantBoundaryPaths = []string{
	"WORK.md",
	".batuta/**",
	".github/**",
	".gitlab-ci.yml",
	".circleci/**",
	"azure-pipelines.yml",
	".buildkite/**",
	"Jenkinsfile",
	"**/LICENSE*",
	"**/COPYING*",
	"**/NOTICE*",
	"**/go.sum",
	"**/package-lock.json",
	"**/npm-shrinkwrap.json",
	"**/yarn.lock",
	"**/pnpm-lock.yaml",
	"**/bun.lockb",
	"**/bun.lock",
	"**/Cargo.lock",
	"**/poetry.lock",
	"**/uv.lock",
	"**/Pipfile.lock",
	"**/Gemfile.lock",
	"**/composer.lock",
}

var (
	grantOperationToken = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	grantBoundaryRules  = compileGrantBoundaryRules()
)

// TaskGrant is a standing authorization an answer carries: paths and operations
// the executor may use until the task ends.
type TaskGrant struct {
	Paths      []string `json:"paths,omitempty"`
	Operations []string `json:"operations,omitempty"`
	Until      string   `json:"until,omitempty"`
}

// IsGrantBoundary says whether a repository-relative path is one of the
// GrantBoundaryPaths.
func IsGrantBoundary(target string) bool {
	target = path.Clean(strings.ReplaceAll(target, "\\", "/"))
	for _, rule := range grantBoundaryRules {
		if rule.MatchString(target) {
			return true
		}
	}
	return false
}

// Normalize returns the grant sorted and deduplicated, with an empty until
// defaulted to task. A grant with no paths and no operations normalizes to the
// zero value.
func (g TaskGrant) Normalize() TaskGrant {
	normalized := TaskGrant{Paths: sortedUnique(g.Paths), Operations: sortedUnique(g.Operations)}
	if len(normalized.Paths) == 0 && len(normalized.Operations) == 0 {
		return TaskGrant{}
	}
	normalized.Until = GrantUntilTask
	return normalized
}

func (g TaskGrant) empty() bool {
	return len(g.Paths) == 0 && len(g.Operations) == 0
}

// ErrInvalidGrant marks a grant that Validate refuses.
var ErrInvalidGrant = errors.New("routing: invalid grant")

func (g TaskGrant) valid() bool {
	return g.Validate() == nil
}

// Validate reports why a grant cannot be recorded. Limits count the
// normalized grant, so a repeated path or operation counts once.
func (g TaskGrant) Validate() error {
	if g.Until != "" && g.Until != GrantUntilTask {
		return fmt.Errorf("%w: until %q: only %q is accepted", ErrInvalidGrant, g.Until, GrantUntilTask)
	}
	for _, grantPath := range g.Paths {
		if !validGrantPath(grantPath) {
			if IsGrantBoundary(grantPath) {
				return fmt.Errorf("%w: path %q is a boundary path a grant never covers", ErrInvalidGrant, grantPath)
			}
			return fmt.Errorf("%w: path %q: want a clean repository-relative glob of at most %d bytes, without .. or backslashes", ErrInvalidGrant, grantPath, maxGrantPathBytes)
		}
	}
	for _, operation := range g.Operations {
		if !grantOperationToken.MatchString(operation) {
			return fmt.Errorf("%w: operation %q: want lowercase letters, digits and dashes, at most 32 bytes", ErrInvalidGrant, operation)
		}
	}
	normalized := g.Normalize()
	if len(normalized.Paths) > maxGrantPaths {
		return fmt.Errorf("%w: %d paths, at most %d", ErrInvalidGrant, len(normalized.Paths), maxGrantPaths)
	}
	if len(normalized.Operations) > maxGrantOperations {
		return fmt.Errorf("%w: %d operations, at most %d", ErrInvalidGrant, len(normalized.Operations), maxGrantOperations)
	}
	return nil
}

func validGrantPath(grantPath string) bool {
	if grantPath == "" || len(grantPath) > maxGrantPathBytes || strings.ContainsAny(grantPath, "\\\x00") ||
		strings.HasPrefix(grantPath, "/") || path.Clean(grantPath) != grantPath {
		return false
	}
	for _, r := range grantPath {
		if r < ' ' || r == 0x7f {
			return false
		}
	}
	if slices.Contains(strings.Split(grantPath, "/"), "..") {
		return false
	}
	if _, err := path.Match(grantPath, ""); err != nil {
		return false
	}
	return !IsGrantBoundary(grantPath)
}

func normalizedGrantPointer(grant *TaskGrant) *TaskGrant {
	if grant == nil {
		return nil
	}
	normalized := grant.Normalize()
	if normalized.empty() {
		return nil
	}
	return &normalized
}

func cloneTaskGrant(grant *TaskGrant) *TaskGrant {
	if grant == nil {
		return nil
	}
	return &TaskGrant{
		Paths:      append([]string(nil), grant.Paths...),
		Operations: append([]string(nil), grant.Operations...),
		Until:      grant.Until,
	}
}

func unionGrants(grants ...TaskGrant) TaskGrant {
	var union TaskGrant
	for _, grant := range grants {
		union.Paths = append(union.Paths, grant.Paths...)
		union.Operations = append(union.Operations, grant.Operations...)
	}
	return union.Normalize()
}

func sortedUnique(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	return slices.Compact(sorted)
}

// compileGrantBoundaryRules turns each boundary glob into an anchored regexp:
// `*` and `?` stay inside one segment, `**/` spans zero or more directories,
// and a trailing `/**` also matches the directory itself.
func compileGrantBoundaryRules() []*regexp.Regexp {
	rules := make([]*regexp.Regexp, len(GrantBoundaryPaths))
	for index, pattern := range GrantBoundaryPaths {
		expression := regexp.QuoteMeta(pattern)
		expression = strings.ReplaceAll(expression, `\*\*/`, `(?:.*/)?`)
		if directory, found := strings.CutSuffix(expression, `/\*\*`); found {
			expression = directory + `(?:/.*)?`
		}
		expression = strings.ReplaceAll(expression, `\*`, `[^/]*`)
		rules[index] = regexp.MustCompile("^" + expression + "$")
	}
	return rules
}
