package loop

import (
	"slices"
	"strings"

	"github.com/batuta-ai/core/gates"
	"github.com/batuta-ai/core/routing"
)

const (
	grantClausePrefix    = "Beyond grant:"
	grantPathsGroup      = "paths:"
	grantOperationsGroup = "operations:"
	grantProbeSuffix     = "__probe__/deep"
)

// grantCoverage is the outcome of checking a question against a standing grant.
type grantCoverage struct {
	Covered   bool
	Malformed bool
	Uncovered []string // items the clause names that the grant does not cover
}

// grantCovers decides whether a grant answers a question. A question is covered
// unless its last non-empty line is a well-formed `Beyond grant: paths: a, b;
// operations: x, y` clause naming at least one ungranted item. A malformed
// clause is never covered, and neither is any question under an empty grant.
func grantCovers(question string, grant routing.TaskGrant) grantCoverage {
	if len(grant.Paths) == 0 && len(grant.Operations) == 0 {
		return grantCoverage{}
	}
	body, found, ok := grantClauseBody(question)
	if !found {
		return grantCoverage{Covered: true}
	}
	if !ok {
		return grantCoverage{Malformed: true}
	}
	paths, operations, ok := parseGrantClause(body)
	if !ok {
		return grantCoverage{Malformed: true}
	}
	var uncovered []string
	for _, item := range paths {
		if !grantCoversPath(grant.Paths, item) {
			uncovered = append(uncovered, item)
		}
	}
	for _, item := range operations {
		if !slices.Contains(grant.Operations, item) {
			uncovered = append(uncovered, item)
		}
	}
	return grantCoverage{Covered: len(uncovered) == 0, Uncovered: uncovered}
}

// grantClauseBody returns the text after `Beyond grant:` on the last non-empty
// line. found is false when the question has no clause; ok is false when the
// clause is not exactly on the last line or has no body.
func grantClauseBody(question string) (body string, found, ok bool) {
	lines := strings.Split(strings.ReplaceAll(question, "\r\n", "\n"), "\n")
	last := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			last = i
			break
		}
	}
	if last < 0 {
		return "", false, false
	}
	for i, line := range lines {
		if i != last && strings.Contains(line, grantClausePrefix) {
			return "", true, false
		}
	}
	line := strings.TrimSpace(lines[last])
	if !strings.Contains(line, grantClausePrefix) {
		return "", false, false
	}
	rest, hasPrefix := strings.CutPrefix(line, grantClausePrefix)
	rest = strings.TrimSpace(rest)
	return rest, true, hasPrefix && rest != ""
}

// parseGrantClause splits a clause body into its optional paths and operations
// groups, which must come in that order, each at most once and non-empty.
func parseGrantClause(body string) (paths, operations []string, ok bool) {
	next := 0 // index of the first group still allowed
	for _, group := range strings.Split(body, ";") {
		group = strings.TrimSpace(group)
		name, rest, hasColon := strings.Cut(group, ":")
		if !hasColon {
			return nil, nil, false
		}
		items, itemsOK := splitGrantItems(rest)
		if !itemsOK {
			return nil, nil, false
		}
		switch name + ":" {
		case grantPathsGroup:
			if next > 0 {
				return nil, nil, false
			}
			for _, item := range items {
				if strings.HasPrefix(item, "/") || strings.Contains(item, "..") {
					return nil, nil, false
				}
			}
			paths, next = items, 1
		case grantOperationsGroup:
			if next > 1 {
				return nil, nil, false
			}
			operations, next = items, 2
		default:
			return nil, nil, false
		}
	}
	return paths, operations, true
}

func splitGrantItems(list string) ([]string, bool) {
	items := strings.Split(list, ",")
	for i, item := range items {
		items[i] = strings.TrimSpace(item)
		if items[i] == "" {
			return nil, false
		}
	}
	return items, true
}

// grantCoversPath matches one clause path against the grant. A directory item
// is covered only when the grant covers every descendant, so it is probed with a
// synthetic deep descendant: `src/x/*` matches the empty remainder of `src/x/`
// but not `src/x/__probe__/deep`.
func grantCoversPath(granted []string, item string) bool {
	if dir, isDir := strings.CutSuffix(item, "/"); isDir {
		return gates.InScope(dir+"/"+grantProbeSuffix, granted)
	}
	return gates.InScope(item, granted)
}
