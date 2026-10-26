package loop

import (
	"reflect"
	"testing"

	"github.com/batuta-ai/core/routing"
)

func TestGrantCovers(t *testing.T) {
	t.Parallel()
	grant := routing.TaskGrant{Paths: []string{"src/**", "Makefile"}, Operations: []string{"install", "migrate"}}
	cases := []struct {
		name      string
		question  string
		covered   bool
		malformed bool
		uncovered []string
	}{
		{name: "no clause", question: "Which name should the flag have?", covered: true},
		{name: "empty question", question: "", covered: true},
		{name: "paths and operations granted", question: "Need more.\nBeyond grant: paths: src/a.go, Makefile; operations: install", covered: true},
		{name: "paths only", question: "Beyond grant: paths: src/a.go", covered: true},
		{name: "operations only", question: "Beyond grant: operations: migrate, install", covered: true},
		{name: "trailing blank lines", question: "Q?\nBeyond grant: paths: src/a.go\n\n  \n", covered: true},
		{name: "path outside grant", question: "Q?\nBeyond grant: paths: src/a.go, docs/x.md", uncovered: []string{"docs/x.md"}},
		{name: "operation outside grant", question: "Q?\nBeyond grant: operations: install, deploy", uncovered: []string{"deploy"}},
		{name: "both groups outside grant", question: "Beyond grant: paths: docs/x.md; operations: deploy", uncovered: []string{"docs/x.md", "deploy"}},
		{name: "operations compare by equality", question: "Beyond grant: operations: Install", uncovered: []string{"Install"}},
		{name: "empty clause", question: "Q?\nBeyond grant:", malformed: true},
		{name: "blank clause", question: "Q?\nBeyond grant:   ", malformed: true},
		{name: "clause not on last line", question: "Beyond grant: paths: src/a.go\nand something else", malformed: true},
		{name: "clause after the question on one line", question: "May I add a.go? Beyond grant: paths: src/a.go", covered: true},
		{name: "one-line question naming an ungranted path", question: "May I edit docs? Beyond grant: paths: docs/x.md; operations: install", uncovered: []string{"docs/x.md"}},
		{name: "empty clause after the question", question: "May I? Beyond grant:", malformed: true},
		{name: "unknown group", question: "Beyond grant: files: src/a.go", malformed: true},
		{name: "groups out of order", question: "Beyond grant: operations: install; paths: src/a.go", malformed: true},
		{name: "repeated group", question: "Beyond grant: paths: src/a.go; paths: src/b.go", malformed: true},
		{name: "empty group", question: "Beyond grant: paths:", malformed: true},
		{name: "empty group between", question: "Beyond grant: paths: src/a.go;; operations: install", malformed: true},
		{name: "empty item", question: "Beyond grant: paths: src/a.go,, src/b.go", malformed: true},
		{name: "trailing comma", question: "Beyond grant: operations: install,", malformed: true},
		{name: "absolute path", question: "Beyond grant: paths: /etc/passwd", malformed: true},
		{name: "dotdot path", question: "Beyond grant: paths: src/../x.go", malformed: true},
		{name: "missing group colon", question: "Beyond grant: paths src/a.go", malformed: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := grantCovers(tc.question, grant)
			want := grantCoverage{Covered: tc.covered, Malformed: tc.malformed, Uncovered: tc.uncovered}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("grantCovers(%q) = %+v, want %+v", tc.question, got, want)
			}
		})
	}
}

func TestGrantCoversPaths(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		grant   []string
		item    string
		covered bool
	}{
		{name: "Makefile is a path", grant: []string{"Makefile"}, item: "Makefile", covered: true},
		{name: "Dockerfile is a path", grant: []string{"Dockerfile"}, item: "Dockerfile", covered: true},
		{name: "Makefile ungranted", grant: []string{"Dockerfile"}, item: "Makefile"},
		{name: "bare directory covers file", grant: []string{"src/x"}, item: "src/x/a.go", covered: true},
		{name: "bare directory covers directory item", grant: []string{"src/x"}, item: "src/x/", covered: true},
		{name: "recursive glob covers directory item", grant: []string{"src/x/**"}, item: "src/x/", covered: true},
		{name: "parent recursive glob covers directory item", grant: []string{"src/**"}, item: "src/x/", covered: true},
		{name: "single star does not cover directory item", grant: []string{"src/x/*"}, item: "src/x/"},
		{name: "sibling directory not covered", grant: []string{"src/y"}, item: "src/x/"},
		{name: "directory item wider than grant", grant: []string{"src/x/a"}, item: "src/x/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := grantCovers("Beyond grant: paths: "+tc.item, routing.TaskGrant{Paths: tc.grant})
			if got.Malformed || got.Covered != tc.covered {
				t.Fatalf("paths %v item %q = %+v, want covered %v", tc.grant, tc.item, got, tc.covered)
			}
		})
	}
}

func TestGrantCoversEmptyGrant(t *testing.T) {
	t.Parallel()
	questions := []string{
		"Which name?",
		"Beyond grant: paths: src/a.go",
		"Beyond grant: operations: install",
		"Beyond grant:",
	}
	for _, question := range questions {
		if got := grantCovers(question, routing.TaskGrant{}); got.Covered {
			t.Fatalf("empty grant covered %q: %+v", question, got)
		}
	}
}
