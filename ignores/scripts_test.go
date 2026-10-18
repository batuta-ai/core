package ignores

import (
	"fmt"
	"testing"
)

func TestScriptTools(t *testing.T) {
	t.Parallel()
	commands := map[string]string{
		"vitest":     "vitest",
		"jest":       "jest",
		"playwright": "playwright test",
		"prettier":   "prettier",
		"eslint":     "eslint",
		"biome":      "biome",
	}
	wrappers := []struct{ name, format string }{
		{"alone", "%s --run"},
		{"after &&", "echo x && %s"},
		{"after ||", "echo x || %s"},
		{"after semicolon", "echo x; %s"},
		{"after npx", "npx %s"},
		{"after pnpm exec", "pnpm exec %s"},
		{"after yarn", "yarn %s"},
		{"after bunx", "bunx %s"},
	}
	for tool, command := range commands {
		for _, w := range wrappers {
			t.Run(tool+" "+w.name, func(t *testing.T) {
				t.Parallel()
				pkg := &packageJSON{Scripts: map[string]string{"s": fmt.Sprintf(w.format, command)}}
				got := scriptTools(pkg)
				if len(got) != 1 || !got[tool] {
					t.Fatalf("scriptTools(%q) = %v, want only %s", pkg.Scripts["s"], got, tool)
				}
			})
		}
	}

	none := []string{
		"vitest-ui",
		"jest-junit --reporter x",
		"playwright install",
		"playwright && test",
		`echo "vitest"`,
		`echo 'jest'`,
		`echo "playwright" test`,
		`run --grep="eslint"`,
		"",
	}
	for _, script := range none {
		t.Run("none "+script, func(t *testing.T) {
			t.Parallel()
			pkg := &packageJSON{Scripts: map[string]string{"s": script}}
			if got := scriptTools(pkg); len(got) != 0 {
				t.Fatalf("scriptTools(%q) = %v, want none", script, got)
			}
		})
	}

	t.Run("nil package", func(t *testing.T) {
		t.Parallel()
		if got := scriptTools(nil); len(got) != 0 {
			t.Fatalf("scriptTools(nil) = %v, want none", got)
		}
	})
}
