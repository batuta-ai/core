package ignores

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

var runnerDetectors = []detector{
	{tool: "vitest", run: checkVitest},
	{tool: "jest", run: checkJest},
	{tool: "playwright", run: checkPlaywright},
}

// runnerFindings builds a workspace with a .batuta directory and the given
// files, and returns what the test runner detectors report.
func runnerFindings(t *testing.T, files map[string]string) []Finding {
	t.Helper()
	root := tempDir(t)
	if err := os.MkdirAll(filepath.Join(root, ".batuta"), 0o755); err != nil {
		t.Fatalf("create .batuta: %v", err)
	}
	for name, content := range files {
		writeFile(t, root, name, content)
	}
	got, err := check(root, runnerDetectors)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	return got
}

const (
	wantVitestUnknown = ".batuta appears outside test.exclude: check that it is excluded, not included"
	wantVitestMissing = "add '**/.batuta/**' to test.exclude, keeping the defaults: exclude: [...configDefaults.exclude, '**/.batuta/**'] (configDefaults from vitest/config)"
	wantVitestScript  = "create vitest.config.ts with test.exclude: [...configDefaults.exclude, '**/.batuta/**']"

	wantJestTest    = "add '<rootDir>/.batuta/' to testPathIgnorePatterns (keep '/node_modules/')"
	wantJestModule  = "add '<rootDir>/.batuta/' to modulePathIgnorePatterns"
	wantJestUnknown = ".batuta appears outside testPathIgnorePatterns and modulePathIgnorePatterns: check that both ignore '<rootDir>/.batuta/'"
	wantJestBoth    = "add '<rootDir>/.batuta/' to testPathIgnorePatterns (keep '/node_modules/') and to modulePathIgnorePatterns"

	wantPlaywrightNamed    = "testDir names .batuta: point it at the tests directory"
	wantPlaywrightComputed = "testDir is computed: check that it is not the repository root, or add '**/.batuta/**' to testIgnore"
	wantPlaywrightUnknown  = ".batuta appears outside testIgnore: check that the tests skip it"
	wantPlaywrightMissing  = "set testDir to the tests directory, or add '**/.batuta/**' to testIgnore"
	wantPlaywrightScript   = "create playwright.config.ts with testDir set to the tests directory"
)

func TestCheckTestRunners(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		files map[string]string
		want  Finding
	}{
		{
			name:  "vitest ignored",
			files: map[string]string{"vitest.config.ts": `export default { test: { exclude: ['**/node_modules/**', '**/.batuta/**'] } }`},
			want:  Finding{Tool: "vitest", Config: "vitest.config.ts", Status: Ignored},
		},
		{
			name:  "vitest stray literal",
			files: map[string]string{"vitest.config.ts": `const dir = '.batuta'; export default { test: {} }`},
			want:  Finding{Tool: "vitest", Config: "vitest.config.ts", Status: Unknown, Hint: wantVitestUnknown},
		},
		{
			name:  "vitest no literal",
			files: map[string]string{"vitest.config.mts": `export default { test: { globals: true } }`},
			want:  Finding{Tool: "vitest", Config: "vitest.config.mts", Status: Missing, Hint: wantVitestMissing},
		},
		{
			name:  "vite config ignored",
			files: map[string]string{"vite.config.ts": `export default { test: { exclude: ['.batuta/**'] } }`},
			want:  Finding{Tool: "vitest", Config: "vite.config.ts", Status: Ignored},
		},
		{
			name:  "vite config stray literal",
			files: map[string]string{"vite.config.js": `export default { test: { include: ['.batuta/**'] } }`},
			want:  Finding{Tool: "vitest", Config: "vite.config.js", Status: Unknown, Hint: wantVitestUnknown},
		},
		{
			name:  "vite config no literal",
			files: map[string]string{"vite.config.ts": `export default { test: { globals: true } }`},
			want:  Finding{Tool: "vitest", Config: "vite.config.ts", Status: Missing, Hint: wantVitestMissing},
		},
		{
			name: "vitest script without config",
			files: map[string]string{
				"package.json": `{"scripts": {"test": "npx vitest run"}}`,
			},
			want: Finding{Tool: "vitest", Config: "package.json", Key: "scripts", Status: Missing, Hint: wantVitestScript},
		},
		{
			name: "vitest config wins over script",
			files: map[string]string{
				"package.json":     `{"scripts": {"test": "vitest"}}`,
				"vitest.config.ts": `export default { test: { exclude: ['.batuta/**'] } }`,
			},
			want: Finding{Tool: "vitest", Config: "vitest.config.ts", Status: Ignored},
		},
	}

	type jestCase struct {
		name string
		body string
		want Finding
	}
	jestCases := []jestCase{
		{
			name: "jest ignored",
			body: `{"testPathIgnorePatterns": ["/node_modules/", "<rootDir>/.batuta/"], "modulePathIgnorePatterns": ["<rootDir>/.batuta/"]}`,
			want: Finding{Tool: "jest", Status: Ignored},
		},
		{
			name: "jest module patterns lack it",
			body: `{"testPathIgnorePatterns": ["<rootDir>/.batuta/"], "modulePathIgnorePatterns": ["/dist/"]}`,
			want: Finding{Tool: "jest", Status: Missing, Hint: wantJestModule},
		},
		{
			name: "jest test patterns lack it",
			body: `{"testPathIgnorePatterns": ["/node_modules/"], "modulePathIgnorePatterns": ["<rootDir>/.batuta/"]}`,
			want: Finding{Tool: "jest", Status: Missing, Hint: wantJestTest},
		},
		{
			name: "jest both lack it, stray literal",
			body: `{"testMatch": ["**/.batuta/**"], "testPathIgnorePatterns": ["/node_modules/"]}`,
			want: Finding{Tool: "jest", Status: Unknown, Hint: wantJestUnknown},
		},
		{
			name: "jest both lack it, no literal",
			body: `{"testEnvironment": "node"}`,
			want: Finding{Tool: "jest", Status: Missing, Hint: wantJestBoth},
		},
	}
	for _, c := range jestCases {
		fromFile := c.want
		fromFile.Config = "jest.config.js"
		tests = append(tests, struct {
			name  string
			files map[string]string
			want  Finding
		}{c.name + " from jest.config.js", map[string]string{"jest.config.js": "module.exports = " + c.body}, fromFile})

		fromKey := c.want
		fromKey.Config, fromKey.Key = "package.json", "jest"
		tests = append(tests, struct {
			name  string
			files map[string]string
			want  Finding
		}{c.name + " from package.json", map[string]string{"package.json": `{"jest": ` + c.body + `}`}, fromKey})
	}
	tests = append(tests, []struct {
		name  string
		files map[string]string
		want  Finding
	}{
		{
			name:  "jest json config",
			files: map[string]string{"jest.config.json": `{"testPathIgnorePatterns": ["<rootDir>/.batuta/"], "modulePathIgnorePatterns": ["<rootDir>/.batuta/"]}`},
			want:  Finding{Tool: "jest", Config: "jest.config.json", Status: Ignored},
		},
		{
			name:  "jest script without config",
			files: map[string]string{"package.json": `{"scripts": {"test": "jest --ci"}}`},
			want:  Finding{Tool: "jest", Config: "package.json", Key: "scripts", Status: Missing, Hint: wantJestBoth},
		},

		{
			name:  "playwright testDir names .batuta",
			files: map[string]string{"playwright.config.ts": `export default { testDir: '.batuta' }`},
			want:  Finding{Tool: "playwright", Config: "playwright.config.ts", Status: Missing, Hint: wantPlaywrightNamed},
		},
		{
			name:  "playwright testDir is another directory",
			files: map[string]string{"playwright.config.ts": `export default { testDir: './e2e' }`},
			want:  Finding{Tool: "playwright", Config: "playwright.config.ts", Status: NotNeeded},
		},
		{
			name:  "playwright testDir is computed",
			files: map[string]string{"playwright.config.ts": `export default { testDir: path.join(__dirname, 'e2e') }`},
			want:  Finding{Tool: "playwright", Config: "playwright.config.ts", Status: Unknown, Hint: wantPlaywrightComputed},
		},
		{
			name:  "playwright no testDir, testIgnore",
			files: map[string]string{"playwright.config.ts": `export default { testIgnore: ['**/.batuta/**'] }`},
			want:  Finding{Tool: "playwright", Config: "playwright.config.ts", Status: Ignored},
		},
		{
			name:  "playwright no testDir, stray literal",
			files: map[string]string{"playwright.config.ts": `export default { testMatch: '**/.batuta/**' }`},
			want:  Finding{Tool: "playwright", Config: "playwright.config.ts", Status: Unknown, Hint: wantPlaywrightUnknown},
		},
		{
			name:  "playwright no testDir, no literal",
			files: map[string]string{"playwright.config.ts": `export default { retries: 1 }`},
			want:  Finding{Tool: "playwright", Config: "playwright.config.ts", Status: Missing, Hint: wantPlaywrightMissing},
		},
		{
			name:  "playwright script without config",
			files: map[string]string{"package.json": `{"scripts": {"e2e": "playwright test"}}`},
			want:  Finding{Tool: "playwright", Config: "package.json", Key: "scripts", Status: Missing, Hint: wantPlaywrightScript},
		},
	}...)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := runnerFindings(t, tt.files)
			if !reflect.DeepEqual(got, []Finding{tt.want}) {
				t.Fatalf("findings = %+v, want %+v", got, []Finding{tt.want})
			}
		})
	}
}

func TestCheckVitestCommentOnly(t *testing.T) {
	t.Parallel()
	got := runnerFindings(t, map[string]string{
		"vitest.config.ts": "// keep .batuta out of the run\n/* exclude '.batuta/**' */\nexport default { test: {} }\n",
	})
	want := []Finding{{Tool: "vitest", Config: "vitest.config.ts", Status: Missing, Hint: wantVitestMissing}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("findings = %+v, want %+v", got, want)
	}
}

func TestCheckVitestKeyedLiteral(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		src    string
		status string
		hint   string
	}{
		{
			name:   "literal only in include",
			src:    `export default { test: { include: ['.batuta/**/*.test.ts'], exclude: ['**/node_modules/**'] } }`,
			status: Unknown,
			hint:   wantVitestUnknown,
		},
		{
			name:   "literal in exclude beside include",
			src:    `export default { test: { include: ['src/**/*.test.ts'], exclude: ['**/.batuta/**'] } }`,
			status: Ignored,
		},
		{
			name:   "literal in exclude before include",
			src:    `export default { test: { exclude: ['**/.batuta/**'], include: ['.batuta-old/**'] } }`,
			status: Ignored,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := runnerFindings(t, map[string]string{"vitest.config.ts": tt.src})
			want := []Finding{{Tool: "vitest", Config: "vitest.config.ts", Status: tt.status, Hint: tt.hint}}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("findings = %+v, want %+v", got, want)
			}
		})
	}
}

func TestCheckViteConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		src  string
		want []Finding
	}{
		{
			name: "imports vitest/config",
			src:  "import { defineConfig } from 'vitest/config'\nexport default defineConfig({})\n",
			want: []Finding{{Tool: "vitest", Config: "vite.config.ts", Status: Missing, Hint: wantVitestMissing}},
		},
		{
			name: "references vitest",
			src:  "/// <reference types='vitest' />\nimport x from 'vitest'\nexport default {}\n",
			want: []Finding{{Tool: "vitest", Config: "vite.config.ts", Status: Missing, Hint: wantVitestMissing}},
		},
		{
			name: "only a vitest-like plugin",
			src:  "import p from 'vitest-plugin-x'\nexport default { plugins: [p()] }\n",
			want: []Finding{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := runnerFindings(t, map[string]string{"vite.config.ts": tt.src})
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("findings = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestCheckPlaywrightTestDir(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		testDir string
		status  string
		hint    string
		ignore  bool
	}{
		{name: "dot", testDir: `'.'`, status: Missing, hint: wantPlaywrightMissing},
		{name: "dot slash", testDir: `'./'`, status: Missing, hint: wantPlaywrightMissing},
		{name: "empty", testDir: `''`, status: Missing, hint: wantPlaywrightMissing},
		{name: "parent", testDir: `'..'`, status: Missing, hint: wantPlaywrightMissing},
		{name: "walks back out", testDir: `'tests/..'`, status: Missing, hint: wantPlaywrightMissing},
		{name: "absolute", testDir: `'/srv/app'`, status: Missing, hint: wantPlaywrightMissing},
		{name: "glob first segment", testDir: `'*/e2e'`, status: Missing, hint: wantPlaywrightMissing},
		{name: "dot with testIgnore", testDir: `'.'`, status: Ignored, ignore: true},
		{name: "tests directory", testDir: `'./tests'`, status: NotNeeded},
		{name: "similar name", testDir: `'.batuta-old'`, status: NotNeeded},
		{name: "batuta", testDir: `'.batuta'`, status: Missing, hint: wantPlaywrightNamed},
		{name: "inside batuta", testDir: `'./.batuta/e2e'`, status: Missing, hint: wantPlaywrightNamed},
		{name: "expression", testDir: `process.env.DIR`, status: Unknown, hint: wantPlaywrightComputed},
		{name: "template with substitution", testDir: "`${base}/e2e`", status: Unknown, hint: wantPlaywrightComputed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			src := "export default { testDir: " + tt.testDir
			if tt.ignore {
				src += ", testIgnore: ['**/.batuta/**']"
			}
			src += " }\n"
			got := runnerFindings(t, map[string]string{"playwright.config.ts": src})
			want := []Finding{{Tool: "playwright", Config: "playwright.config.ts", Status: tt.status, Hint: tt.hint}}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("findings = %+v, want %+v", got, want)
			}
		})
	}
}

func TestCheckTestRunnerLexError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		files map[string]string
		want  Finding
	}{
		{
			name:  "vitest",
			files: map[string]string{"vitest.config.ts": "export default { test: { name: 'open } }\n"},
			want:  Finding{Tool: "vitest", Config: "vitest.config.ts", Status: Unknown, Hint: "unterminated string"},
		},
		{
			name:  "vite config",
			files: map[string]string{"vite.config.ts": "/* never closed\nexport default {}\n"},
			want:  Finding{Tool: "vitest", Config: "vite.config.ts", Status: Unknown, Hint: "unterminated block comment"},
		},
		{
			name:  "jest",
			files: map[string]string{"jest.config.js": "module.exports = { a: `open }\n"},
			want:  Finding{Tool: "jest", Config: "jest.config.js", Status: Unknown, Hint: "unterminated template"},
		},
		{
			name:  "playwright",
			files: map[string]string{"playwright.config.ts": "export default { testDir: 'e2e }\n"},
			want:  Finding{Tool: "playwright", Config: "playwright.config.ts", Status: Unknown, Hint: "unterminated string"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := runnerFindings(t, tt.files)
			if !reflect.DeepEqual(got, []Finding{tt.want}) {
				t.Fatalf("findings = %+v, want %+v", got, []Finding{tt.want})
			}
		})
	}
}

func TestCheckTestRunnersRootOnly(t *testing.T) {
	t.Parallel()
	const vitest = `export default { test: { globals: true } }`
	const jest = `module.exports = { testEnvironment: 'node' }`
	got := runnerFindings(t, map[string]string{
		"vitest.config.ts":                    vitest,
		"packages/a/vitest.config.ts":         vitest,
		"packages/a/jest.config.js":           jest,
		"node_modules/x/vitest.config.ts":     vitest,
		"node_modules/x/jest.config.js":       jest,
		"packages/a/playwright.config.ts":     `export default {}`,
		"node_modules/x/playwright.config.ts": `export default {}`,
	})
	want := []Finding{{Tool: "vitest", Config: "vitest.config.ts", Status: Missing, Hint: wantVitestMissing}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("findings = %+v, want %+v", got, want)
	}
}

func TestCheckTestRunnersSegmentMatch(t *testing.T) {
	t.Parallel()
	const jestPatterns = "testPathIgnorePatterns: ['<rootDir>/.batuta-old/'], modulePathIgnorePatterns: ['<rootDir>/.batuta-old/']"
	tests := []struct {
		name   string
		files  map[string]string
		tool   string
		config string
		key    string
	}{
		{
			name:   "vitest exclude",
			files:  map[string]string{"vitest.config.ts": "export default { test: { exclude: ['**/.batuta-old/**'] } }\n"},
			tool:   "vitest",
			config: "vitest.config.ts",
		},
		{
			name:   "playwright testIgnore",
			files:  map[string]string{"playwright.config.ts": "export default { testIgnore: ['**/.batuta-old/**'] }\n"},
			tool:   "playwright",
			config: "playwright.config.ts",
		},
		{
			name:   "jest config file",
			files:  map[string]string{"jest.config.js": "module.exports = { " + jestPatterns + " }\n"},
			tool:   "jest",
			config: "jest.config.js",
		},
		{
			name: "jest key of package.json",
			files: map[string]string{"package.json": `{"jest": {` +
				`"testPathIgnorePatterns": ["<rootDir>/.batuta-old/"], ` +
				`"modulePathIgnorePatterns": ["<rootDir>/.batuta-old/"]}}`},
			tool:   "jest",
			config: "package.json",
			key:    "jest",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := runnerFindings(t, tt.files)
			if len(got) != 1 || got[0].Tool != tt.tool || got[0].Config != tt.config || got[0].Key != tt.key {
				t.Fatalf("findings = %+v, want one %s finding on %s", got, tt.tool, tt.config)
			}
			if got[0].Status == Ignored {
				t.Fatalf("status = %q for a pattern that only names .batuta-old", got[0].Status)
			}
		})
	}
}

func TestCheckPlaywrightTestDirClean(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		testDir string
		status  string
		hint    string
	}{
		{name: "dot segments", testDir: `'././.batuta'`, status: Missing, hint: wantPlaywrightNamed},
		{name: "walks into batuta", testDir: `'./tests/../.batuta'`, status: Missing, hint: wantPlaywrightNamed},
		{name: "walks back to tests", testDir: `'./tests/../tests'`, status: NotNeeded},
		{name: "walks out of the root", testDir: `'./tests/../../x'`, status: Missing, hint: wantPlaywrightMissing},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			src := "export default { testDir: " + tt.testDir + " }\n"
			got := runnerFindings(t, map[string]string{"playwright.config.ts": src})
			want := []Finding{{Tool: "playwright", Config: "playwright.config.ts", Status: tt.status, Hint: tt.hint}}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("findings = %+v, want %+v", got, want)
			}
		})
	}
}
