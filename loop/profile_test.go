package loop

import "testing"

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
