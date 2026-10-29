package loop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProfileConventionsSection(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"absent", "Stack: Go\nMethodology: TDD\n", ""},
		{"only section", "Stack: Go\n\n## Conventions\nNo globals.\n", "No globals."},
		{"stops at next heading", "## Conventions\nNo globals.\nPrefer tables.\n\n## Notes\nignored\n", "No globals.\nPrefer tables."},
		{"not the briefs heading", "## Conventions for briefs\nfrom a template\n", ""},
		{"exact heading after the briefs one", "## Conventions for briefs\nfrom a template\n\n## Conventions\nOwn rule.\n", "Own rule."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ParseProfile(tt.raw).ConventionsSection(); got != tt.want {
				t.Errorf("ConventionsSection() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestProfileConventionsWarning(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"missing", "Stack: Go\n", ""},
		{"empty", "## Conventions\n\n## Notes\nOther text\n", ""},
		{"below ceiling", "## Conventions\n" + strings.Repeat("a", 1599), ""},
		{"at ceiling", "## Conventions\n" + strings.Repeat("a", 1600), ""},
		{"above ceiling", "## Conventions\n" + strings.Repeat("a", 1601), "profile Conventions section is 1601 UTF-8 bytes (limit 1600)"},
		{"multibyte at ceiling", "## Conventions\n" + strings.Repeat("é", 800), ""},
		{"multibyte above ceiling", "## Conventions\n" + strings.Repeat("é", 801), "profile Conventions section is 1602 UTF-8 bytes (limit 1600)"},
		{"only extracted section", "## Conventions\nShort.\n\n## Notes\n" + strings.Repeat("a", 1601), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ParseProfile(tt.raw).conventionsWarning(); got != tt.want {
				t.Errorf("conventionsWarning() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseProfileWorktreeHome(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"missing", "Stack: Go\n", "repository"},
		{"explicit repository", "Worktree-home: repository\n", "repository"},
		{"cache lowercase", "Worktree-home: cache\n", "cache"},
		{"cache uppercase", "Worktree-home: CACHE\n", "cache"},
		{"cache mixed case", "Worktree-home: Cache\n", "cache"},
		{"case insensitive key", "worktree-home: cache\n", "cache"},
		{"first occurrence wins", "Worktree-home: cache\nWorktree-home: repository\n", "cache"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ParseProfile(tt.raw).WorktreeHome; got != tt.want {
				t.Errorf("WorktreeHome = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLoadProfileRejectsWorktreeHome(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		content    string
		wantErrMsg string
	}{
		{
			name:       "invalid value foo",
			content:    "Worktree-home: foo\n",
			wantErrMsg: "Worktree-home",
		},
		{
			name:       "invalid value invalid",
			content:    "Worktree-home: invalid\n",
			wantErrMsg: "Worktree-home",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, ".batuta"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, ".batuta", "profile.md"), []byte(tt.content), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := LoadProfile(dir)
			if err == nil {
				t.Fatalf("LoadProfile() expected error, got nil")
			}
			errStr := err.Error()
			if !strings.Contains(errStr, "Worktree-home") {
				t.Errorf("LoadProfile() error %q does not name 'Worktree-home'", errStr)
			}
			if !strings.Contains(errStr, "repository") || !strings.Contains(errStr, "cache") {
				t.Errorf("LoadProfile() error %q does not name both accepted values ('repository' and 'cache')", errStr)
			}
		})
	}
}
