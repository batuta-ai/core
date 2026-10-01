package questions

import "testing"

func TestKindWords(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		text string
		want string
	}{
		{"sandbox", "The SANDBOX blocks this", "environment"},
		{"docker", "Docker is unavailable", "environment"},
		{"colima", "colima will not start", "environment"},
		{"socket", "socket refused", "environment"},
		{"not permitted", "operation NOT PERMITTED", "environment"},
		{"permission", "permission denied", "environment"},
		{"gocache", "GOCACHE cannot be written", "environment"},
		{"network", "network failed", "environment"},
		{"loopback", "loopback is blocked", "environment"},
		{"bin ps", "/bin/ps cannot run", "environment"},
		{"write access", "no WRITE ACCESS", "environment"},
		{"connectivity", "connectivity failed", "environment"},
		{"container", "container absent", "environment"},
		{"disk", "disk full", "environment"},
		{"disco", "disco cheio", "environment"},
		{"ambiente", "ambiente indisponível", "environment"},
		{"environment", "environment failed", "environment"},
		{"toolchain", "TOOLCHAIN absent", "environment"},
		{"scope", "May I widen SCOPE?", "scope_change"},
		{"escopo", "mudar escopo?", "scope_change"},
		{"install", "install a package?", "scope_change"},
		{"instalar", "instalar pacote?", "scope_change"},
		{"dependenc", "new dependency?", "scope_change"},
		{"stop condition", "past the STOP CONDITION?", "continue"},
		{"continue past", "continue past failure?", "continue"},
		{"may i continue", "MAY I CONTINUE?", "continue"},
		{"may i resume", "may I resume?", "continue"},
		{"posso continuar", "posso continuar?", "continue"},
		{"posso retomar", "posso retomar?", "continue"},
		{"pode retomar", "pode retomar?", "continue"},
		{"nova tentativa", "nova tentativa?", "continue"},
		{"retry", "retry this?", "continue"},
		{"resume this", "resume this task?", "continue"},
		{"retomar est", "retomar esta tarefa?", "continue"},
		{"resume verification", "resume verification?", "continue"},
		{"continue verification", "continue verification?", "continue"},
		{"other", "Which rule applies?", "other"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := Kind(tt.text, nil); got != tt.want {
				t.Errorf("Kind(%q) = %q, want %q", tt.text, got, tt.want)
			}
		})
	}
}

func TestKindPath(t *testing.T) {
	t.Parallel()
	for _, extension := range []string{".go", ".ts", ".tsx", ".js", ".php", ".md", ".json", ".yaml", ".yml", ".lock", ".sh"} {
		t.Run(extension, func(t *testing.T) {
			t.Parallel()
			path := "src/other/file" + extension
			if got := Kind("May I edit `"+path+"`?", []string{"src/owned"}); got != "scope_change" {
				t.Errorf("outside path %q: got %q", path, got)
			}
			if got := Kind("May I edit `"+path+"`?", []string{"src/other"}); got != "other" {
				t.Errorf("inside directory %q: got %q", path, got)
			}
			if got := Kind("May I edit `"+path+"`?", []string{"src/other/*" + extension}); got != "other" {
				t.Errorf("inside glob %q: got %q", path, got)
			}
			if got := Kind("May I edit `"+path+"`?", []string{path}); got != "other" {
				t.Errorf("exact path %q: got %q", path, got)
			}
		})
	}
	for _, tt := range []struct {
		name string
		text string
		want string
	}{
		{"no slash", "May I edit file.go?", "other"},
		{"no extension", "May I edit src/file.txt?", "other"},
		{"sibling directory", "May I edit src/owned-extra/file.go?", "scope_change"},
		{"glob mismatch", "May I edit src/other/file.go?", "scope_change"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := Kind(tt.text, []string{"src/owned", "src/other/*.ts"}); got != tt.want {
				t.Errorf("Kind(%q) = %q, want %q", tt.text, got, tt.want)
			}
		})
	}
}

func TestKindPathTrailingPeriod(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name  string
		text  string
		scope []string
		want  string
	}{
		{"outside", "May I edit src/outside.go.", []string{"src/owned"}, "scope_change"},
		{"outside quoted", "Should I touch `src/outside.go`.", []string{"src/owned"}, "scope_change"},
		{"inside", "May I edit src/owned/file.go.", []string{"src/owned"}, "other"},
		{"inside exact", "May I edit src/owned/file.go.", []string{"src/owned/file.go"}, "other"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := Kind(tt.text, tt.scope); got != tt.want {
				t.Errorf("Kind(%q) = %q, want %q", tt.text, got, tt.want)
			}
		})
	}
}

func TestKindPrecedence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		text string
		want string
	}{
		{"environment before scope", "Docker blocks changes beyond Scope", "environment"},
		{"environment before path", "Docker blocks src/new/file.go", "environment"},
		{"environment before continue", "May I continue past the sandbox failure?", "environment"},
		{"scope before continue", "May I continue past the Scope limit?", "scope_change"},
		{"path before continue", "May I continue past src/new/file.go?", "scope_change"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := Kind(tt.text, []string{"src/owned"}); got != tt.want {
				t.Errorf("Kind(%q) = %q, want %q", tt.text, got, tt.want)
			}
		})
	}
}
