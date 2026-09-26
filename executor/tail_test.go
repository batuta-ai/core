package executor

import "testing"

func TestRedactPathsKeepsRelative(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, in, workspace, want string }{
		{"relative path", "edit a/b.go now", "", "edit a/b.go now"},
		{"dotted relative path", "see ./a/b.go and ../c/d.go", "", "see ./a/b.go and ../c/d.go"},
		{"absolute path", "open /private/x", "", "open x"},
		{"absolute path at start", "/private/x failed", "", "x failed"},
		{"trailing punctuation", "open /private/x.", "", "open x."},
		{"workspace prefix", "open /work/tree/a/b.go", "/work/tree", "open a/b.go"},
		{"windows separator", `open a\b.go`, "", `open a\b.go`},
		{"empty", "", "/work", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := RedactPaths(tc.in, tc.workspace); got != tc.want {
				t.Fatalf("RedactPaths(%q, %q) = %q, want %q", tc.in, tc.workspace, got, tc.want)
			}
		})
	}
}

func TestRedactWorkspaceBoundary(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, in, workspace, want string }{
		{"sibling with shared prefix", "open /work/spacefoo", "/work/space", "open spacefoo"},
		{"followed by separator", "open /work/space/a.go", "/work/space", "open a.go"},
		{"followed by quote", `open "/work/space"`, "/work/space", `open ""`},
		{"followed by whitespace", "cd /work/space now", "/work/space", "cd  now"},
		{"at end of string", "cd /work/space", "/work/space", "cd "},
		{"followed by period", "cannot open /work/space.", "/work/space", "cannot open ."},
		{"followed by comma", "in /work/space, then", "/work/space", "in , then"},
		{"followed by semicolon", "cd /work/space; ls", "/work/space", "cd ; ls"},
		{"followed by colon", "/work/space: not found", "/work/space", ": not found"},
		{"followed by parenthesis", "(cwd /work/space)", "/work/space", "(cwd )"},
		{"trailing separator on workspace", "open /work/space/a.go", "/work/space/", "open a.go"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := RedactPaths(tc.in, tc.workspace); got != tc.want {
				t.Fatalf("RedactPaths(%q, %q) = %q, want %q", tc.in, tc.workspace, got, tc.want)
			}
		})
	}
}

func TestDropSecretLines(t *testing.T) {
	t.Parallel()
	in := "kept\nAPI_KEY=abc\n  TOKEN=x\nlower=case\nkept too"
	if got, want := DropSecretLines(in), "kept\nlower=case\nkept too"; got != want {
		t.Fatalf("DropSecretLines = %q, want %q", got, want)
	}
	if got := DropSecretLines(""); got != "" {
		t.Fatalf("DropSecretLines(empty) = %q", got)
	}
}
