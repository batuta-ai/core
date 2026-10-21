package ignores

import (
	"slices"
	"sort"
	"testing"
)

func TestRegistryMatchesTools(t *testing.T) {
	t.Parallel()

	var detected []string
	for _, d := range defaultDetectors {
		detected = append(detected, d.tool)
	}
	sort.Strings(detected)
	literal := []string{"biome", "eslint", "jest", "playwright", "prettier", "pytest", "typescript", "vitest"}
	tools := slices.Sorted(slices.Values(Tools))
	if !slices.Equal(detected, literal) {
		t.Fatalf("default detectors = %v, want %v", detected, literal)
	}
	if !slices.Equal(tools, literal) {
		t.Fatalf("Tools = %v, want %v", tools, literal)
	}
}

func TestCheckEveryTool(t *testing.T) {
	t.Parallel()

	root := tempDir(t)
	writeFile(t, root, ".batuta/.keep", "")
	writeFile(t, root, "vitest.config.ts", "export default {}\n")
	writeFile(t, root, "jest.config.js", "module.exports = {}\n")
	writeFile(t, root, "playwright.config.ts", "export default {}\n")
	writeFile(t, root, "eslint.config.js", "export default []\n")
	writeFile(t, root, ".prettierrc", "{}\n")
	writeFile(t, root, "biome.json", "{}\n")
	writeFile(t, root, "tsconfig.json", "{}\n")
	writeFile(t, root, "pytest.ini", "")

	findings, err := Check(root)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	seen := map[string]bool{}
	for _, f := range findings {
		seen[f.Tool] = true
	}
	var got []string
	for tool := range seen {
		got = append(got, tool)
	}
	sort.Strings(got)
	want := slices.Sorted(slices.Values(Tools))
	if !slices.Equal(got, want) {
		t.Fatalf("finding tools = %v, want %v\nfindings: %+v", got, want, findings)
	}
}
