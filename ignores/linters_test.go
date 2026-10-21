package ignores

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

var linterDetectors = []detector{
	{tool: "eslint", run: checkESLint},
	{tool: "prettier", run: checkPrettier},
}

// linterFindings builds a workspace with a .batuta directory and the given
// files, and returns what the linter detectors report.
func linterFindings(t *testing.T, files map[string]string) []Finding {
	t.Helper()
	root := tempDir(t)
	if err := os.MkdirAll(filepath.Join(root, ".batuta"), 0o755); err != nil {
		t.Fatalf("create .batuta: %v", err)
	}
	for name, content := range files {
		writeFile(t, root, name, content)
	}
	got, err := check(root, linterDetectors)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	return got
}

const (
	wantESLintUnknown = ".batuta is not in a global ignore: add { ignores: ['.batuta/**'] } as its own element of the config array"
	wantESLintMissing = "add { ignores: ['.batuta/**'] } as its own element of the config array"
	wantESLintLegacy  = "add .batuta/ to .eslintignore"
	wantESLintScript  = "create eslint.config.js with { ignores: ['.batuta/**'] } as its own element of the config array"

	wantPrettierMissing = "add .batuta/ to .prettierignore"
)

func TestCheckLinters(t *testing.T) {
	t.Parallel()
	legacyJSON := `{ "ignorePatterns": [".batuta/"] }`
	legacyJS := `module.exports = { ignorePatterns: ['.batuta/'] }`
	legacyYAML := "ignorePatterns:\n  - .batuta/\n"
	tests := []struct {
		name  string
		files map[string]string
		want  Finding
	}{
		{
			name:  "flat own element",
			files: map[string]string{"eslint.config.js": `export default [{ ignores: ['.batuta/**'] }]`},
			want:  Finding{Tool: "eslint", Config: "eslint.config.js", Status: Ignored},
		},
		{
			name:  "flat globalIgnores",
			files: map[string]string{"eslint.config.mjs": `import { globalIgnores } from 'eslint/config'; export default [globalIgnores(['.batuta/**'])]`},
			want:  Finding{Tool: "eslint", Config: "eslint.config.mjs", Status: Ignored},
		},
		{
			name:  "flat named own element",
			files: map[string]string{"eslint.config.ts": `export default [{ name: 'batuta', ignores: ['.batuta/**'] }]`},
			want:  Finding{Tool: "eslint", Config: "eslint.config.ts", Status: Ignored},
		},
		{
			name:  "flat ignores beside files",
			files: map[string]string{"eslint.config.js": `export default [{ files: ['**/*.js'], ignores: ['.batuta/**'] }]`},
			want:  Finding{Tool: "eslint", Config: "eslint.config.js", Status: Unknown, Hint: wantESLintUnknown},
		},
		{
			name:  "flat stray literal",
			files: map[string]string{"eslint.config.cjs": `const dir = '.batuta'; module.exports = [{ rules: {} }]`},
			want:  Finding{Tool: "eslint", Config: "eslint.config.cjs", Status: Unknown, Hint: wantESLintUnknown},
		},
		{
			name:  "flat no literal",
			files: map[string]string{"eslint.config.js": `export default [{ ignores: ['dist/**'] }]`},
			want:  Finding{Tool: "eslint", Config: "eslint.config.js", Status: Missing, Hint: wantESLintMissing},
		},
		{
			name:  "legacy .eslintrc",
			files: map[string]string{".eslintrc": legacyJSON},
			want:  Finding{Tool: "eslint", Config: ".eslintrc", Status: Ignored},
		},
		{
			name:  "legacy .eslintrc.json",
			files: map[string]string{".eslintrc.json": legacyJSON},
			want:  Finding{Tool: "eslint", Config: ".eslintrc.json", Status: Ignored},
		},
		{
			name:  "legacy .eslintrc.js",
			files: map[string]string{".eslintrc.js": legacyJS},
			want:  Finding{Tool: "eslint", Config: ".eslintrc.js", Status: Ignored},
		},
		{
			name:  "legacy .eslintrc.cjs",
			files: map[string]string{".eslintrc.cjs": legacyJS},
			want:  Finding{Tool: "eslint", Config: ".eslintrc.cjs", Status: Ignored},
		},
		{
			name:  "legacy .eslintrc.yaml",
			files: map[string]string{".eslintrc.yaml": legacyYAML},
			want:  Finding{Tool: "eslint", Config: ".eslintrc.yaml", Status: Ignored},
		},
		{
			name:  "legacy .eslintrc.yml",
			files: map[string]string{".eslintrc.yml": legacyYAML},
			want:  Finding{Tool: "eslint", Config: ".eslintrc.yml", Status: Ignored},
		},
		{
			name:  "legacy .eslintrc as yaml",
			files: map[string]string{".eslintrc": legacyYAML},
			want:  Finding{Tool: "eslint", Config: ".eslintrc", Status: Ignored},
		},
		{
			name: "legacy ignorePatterns string",
			files: map[string]string{
				".eslintrc.json": `{ "ignorePatterns": ".batuta/" }`,
			},
			want: Finding{Tool: "eslint", Config: ".eslintrc.json", Status: Ignored},
		},
		{
			name: "legacy eslintignore",
			files: map[string]string{
				".eslintrc.json": `{ "root": true }`,
				".eslintignore":  "dist\n.batuta/\n",
			},
			want: Finding{Tool: "eslint", Config: ".eslintrc.json", Status: Ignored},
		},
		{
			name:  "legacy eslintConfig key ignored",
			files: map[string]string{"package.json": `{ "eslintConfig": { "ignorePatterns": [".batuta/"] } }`},
			want:  Finding{Tool: "eslint", Config: "package.json", Key: "eslintConfig", Status: Ignored},
		},
		{
			name:  "legacy eslintConfig key missing",
			files: map[string]string{"package.json": `{ "eslintConfig": { "root": true } }`},
			want:  Finding{Tool: "eslint", Config: "package.json", Key: "eslintConfig", Status: Missing, Hint: wantESLintLegacy},
		},
		{
			name:  "legacy json missing",
			files: map[string]string{".eslintrc.json": `{ "root": true }`},
			want:  Finding{Tool: "eslint", Config: ".eslintrc.json", Status: Missing, Hint: wantESLintLegacy},
		},
		{
			name:  "legacy js missing",
			files: map[string]string{".eslintrc.js": `module.exports = { root: true }`},
			want:  Finding{Tool: "eslint", Config: ".eslintrc.js", Status: Missing, Hint: wantESLintLegacy},
		},
		{
			name:  "legacy yaml missing",
			files: map[string]string{".eslintrc.yml": "root: true\n"},
			want:  Finding{Tool: "eslint", Config: ".eslintrc.yml", Status: Missing, Hint: wantESLintLegacy},
		},
		{
			name:  "legacy ignored only by a commented eslintignore line",
			files: map[string]string{".eslintrc.json": `{}`, ".eslintignore": "# .batuta/\n"},
			want:  Finding{Tool: "eslint", Config: ".eslintrc.json", Status: Missing, Hint: wantESLintLegacy},
		},
		{
			name:  "eslint script only",
			files: map[string]string{"package.json": `{ "scripts": { "lint": "eslint ." } }`},
			want:  Finding{Tool: "eslint", Config: "package.json", Key: "scripts", Status: Missing, Hint: wantESLintScript},
		},
		{
			name:  "prettier rc ignored by prettierignore",
			files: map[string]string{".prettierrc": `{}`, ".prettierignore": ".batuta/\n"},
			want:  Finding{Tool: "prettier", Config: ".prettierrc", Status: Ignored},
		},
		{
			name:  "prettier config file missing",
			files: map[string]string{"prettier.config.mjs": `export default {}`},
			want:  Finding{Tool: "prettier", Config: "prettier.config.mjs", Status: Missing, Hint: wantPrettierMissing},
		},
		{
			name:  "prettier key ignored",
			files: map[string]string{"package.json": `{ "prettier": { "semi": false } }`, ".prettierignore": "**/.batuta/**\n"},
			want:  Finding{Tool: "prettier", Config: "package.json", Key: "prettier", Status: Ignored},
		},
		{
			name:  "prettier key missing",
			files: map[string]string{"package.json": `{ "prettier": {} }`, ".prettierignore": "dist\n"},
			want:  Finding{Tool: "prettier", Config: "package.json", Key: "prettier", Status: Missing, Hint: wantPrettierMissing},
		},
		{
			name:  "prettier script ignored",
			files: map[string]string{"package.json": `{ "scripts": { "fmt": "prettier --write ." } }`, ".prettierignore": "/.batuta\n"},
			want:  Finding{Tool: "prettier", Config: "package.json", Key: "scripts", Status: Ignored},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := linterFindings(t, tt.files)
			if !reflect.DeepEqual(got, []Finding{tt.want}) {
				t.Fatalf("findings = %+v, want [%+v]", got, tt.want)
			}
		})
	}
}

func TestCheckESLintCommentOnly(t *testing.T) {
	t.Parallel()
	src := "// ignore .batuta/ here\n/* { ignores: ['.batuta/**'] } */\nexport default [{ rules: {} }]\n"
	got := linterFindings(t, map[string]string{"eslint.config.js": src})
	want := []Finding{{Tool: "eslint", Config: "eslint.config.js", Status: Missing, Hint: wantESLintMissing}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("findings = %+v, want %+v", got, want)
	}
}

func TestCheckESLintGlobalIgnores(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		source string
		want   string
	}{
		{"own element", `export default [{ ignores: ['.batuta/**'] }]`, Ignored},
		{"globalIgnores call", `import { globalIgnores } from 'eslint/config'
export default [globalIgnores(['.batuta/**'])]`, Ignored},
		{"ignores beside files", `export default [{ files: ['**/*.ts'], ignores: ['.batuta/**'] }]`, Unknown},
		{"ignores beside rules", `export default [{ ignores: ['.batuta/**'], rules: {} }]`, Unknown},
		{"literal outside ignores", `export default [{ files: ['.batuta/**'] }, { ignores: ['dist/**'] }]`, Unknown},
		{"literal outside globalIgnores", `const x = '.batuta'; export default [globalIgnores(['dist'])]`, Unknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := linterFindings(t, map[string]string{"eslint.config.js": tt.source})
			if len(got) != 1 || got[0].Status != tt.want {
				t.Fatalf("findings = %+v, want one %s", got, tt.want)
			}
		})
	}
}

func TestIgnoreFileLines(t *testing.T) {
	t.Parallel()
	tests := []struct {
		line string
		want bool
	}{
		{".batuta", true},
		{".batuta/", true},
		{"/.batuta/", true},
		{"**/.batuta/**", true},
		{"  .batuta/  ", true},
		{".batuta/\r", true},
		{"# .batuta", false},
		{"!.batuta/", false},
		{".batuta-old", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			t.Parallel()
			if got := ignoreFileMatches(tt.line); got != tt.want {
				t.Fatalf("ignoreFileMatches(%q) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

func TestCheckPrettierGitignore(t *testing.T) {
	t.Parallel()
	got := linterFindings(t, map[string]string{".prettierrc.json": `{}`, ".gitignore": "node_modules\n.batuta/\n"})
	want := []Finding{{Tool: "prettier", Config: ".prettierrc.json", Status: Ignored}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("findings = %+v, want %+v", got, want)
	}
}

func TestCheckScriptOnlyLinters(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		tool string
		want Finding
	}{
		{"prettier", "prettier --check .", Finding{Tool: "prettier", Config: "package.json", Key: "scripts", Status: Missing, Hint: wantPrettierMissing}},
		{"eslint", "eslint src", Finding{Tool: "eslint", Config: "package.json", Key: "scripts", Status: Missing, Hint: wantESLintScript}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := linterFindings(t, map[string]string{"package.json": `{ "scripts": { "lint": "` + tt.tool + `" } }`})
			if !reflect.DeepEqual(got, []Finding{tt.want}) {
				t.Fatalf("findings = %+v, want [%+v]", got, tt.want)
			}
		})
	}
}

func TestCheckLintersRootOnly(t *testing.T) {
	t.Parallel()
	got := linterFindings(t, map[string]string{
		"eslint.config.js":                `export default [{ rules: {} }]`,
		"packages/a/eslint.config.js":     `export default [{ rules: {} }]`,
		"packages/a/.prettierrc":          `{}`,
		"node_modules/x/eslint.config.js": `export default [{ rules: {} }]`,
		"node_modules/x/.prettierrc":      `{}`,
	})
	want := []Finding{{Tool: "eslint", Config: "eslint.config.js", Status: Missing, Hint: wantESLintMissing}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("findings = %+v, want %+v", got, want)
	}
}
