package ignores

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

var biomeDetectors = []detector{{tool: "biome", run: checkBiome}}

// biomeFindings builds a workspace with a .batuta directory and the given
// files, and returns what the Biome detector reports.
func biomeFindings(t *testing.T, files map[string]string) []Finding {
	t.Helper()
	root := tempDir(t)
	if err := os.MkdirAll(filepath.Join(root, ".batuta"), 0o755); err != nil {
		t.Fatalf("create .batuta: %v", err)
	}
	for name, content := range files {
		writeFile(t, root, name, content)
	}
	got, err := check(root, biomeDetectors)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	return got
}

const (
	wantBiomeV2Includes = `add "!**/.batuta" to files.includes, after the positive patterns`
	wantBiomeV2Absent   = `add "files": { "includes": ["**", "!**/.batuta"] }`
	wantBiomeV1         = `add ".batuta" to files.ignore`
	wantBiomeUnknown    = `version unknown: v2 adds "!**/.batuta" to files.includes after "**", v1 adds ".batuta" to files.ignore`
	wantBiomeScript     = `create biome.json: v2 "files": { "includes": ["**", "!**/.batuta"] }, v1 "files": { "ignore": [".batuta"] }`
)

func biomeSchema(version string) string {
	return `"$schema": "https://biomejs.dev/schemas/` + version + `/schema.json"`
}

func TestCheckBiome(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		files map[string]string
		want  Finding
	}{
		{
			name:  "v2 negated last match",
			files: map[string]string{"biome.json": `{ ` + biomeSchema("2.0.0") + `, "files": { "includes": ["**", "!**/.batuta"] } }`},
			want:  Finding{Tool: "biome", Config: "biome.json", Status: Ignored},
		},
		{
			name:  "v2 includes without a negation",
			files: map[string]string{"biome.json": `{ ` + biomeSchema("2.0.0") + `, "files": { "includes": ["src/**", ".batuta/**"] } }`},
			want:  Finding{Tool: "biome", Config: "biome.json", Status: Missing, Hint: wantBiomeV2Includes},
		},
		{
			name:  "v2 includes naming only other paths",
			files: map[string]string{"biome.json": `{ ` + biomeSchema("2.0.0") + `, "files": { "includes": ["src/**"] } }`},
			want:  Finding{Tool: "biome", Config: "biome.json", Status: Missing, Hint: wantBiomeV2Includes},
		},
		{
			name:  "v2 no includes",
			files: map[string]string{"biome.json": `{ ` + biomeSchema("2.0.0") + ` }`},
			want:  Finding{Tool: "biome", Config: "biome.json", Status: Missing, Hint: wantBiomeV2Absent},
		},
		{
			name:  "v1 files.ignore entry",
			files: map[string]string{"biome.json": `{ ` + biomeSchema("1.9.4") + `, "files": { "ignore": ["dist", ".batuta"] } }`},
			want:  Finding{Tool: "biome", Config: "biome.json", Status: Ignored},
		},
		{
			name:  "v1 files.ignore entry with a .batuta segment",
			files: map[string]string{"biome.json": `{ ` + biomeSchema("1.9.4") + `, "files": { "ignore": ["**/.batuta/**"] } }`},
			want:  Finding{Tool: "biome", Config: "biome.json", Status: Ignored},
		},
		{
			name:  "v1 files.ignore without an entry",
			files: map[string]string{"biome.json": `{ ` + biomeSchema("1.9.4") + `, "files": { "ignore": ["dist"] } }`},
			want:  Finding{Tool: "biome", Config: "biome.json", Status: Missing, Hint: wantBiomeV1},
		},
		{
			name:  "v1 no files",
			files: map[string]string{"biome.json": `{ ` + biomeSchema("1.9.4") + ` }`},
			want:  Finding{Tool: "biome", Config: "biome.json", Status: Missing, Hint: wantBiomeV1},
		},
		{
			name:  "unknown version ignored by includes",
			files: map[string]string{"biome.json": `{ "files": { "includes": ["**", "!**/.batuta"] } }`},
			want:  Finding{Tool: "biome", Config: "biome.json", Status: Ignored},
		},
		{
			name:  "unknown version ignored by files.ignore",
			files: map[string]string{"biome.json": `{ "files": { "ignore": [".batuta"] } }`},
			want:  Finding{Tool: "biome", Config: "biome.json", Status: Ignored},
		},
		{
			name:  "unknown version form present",
			files: map[string]string{"biome.json": `{ "files": { "includes": ["**"] } }`},
			want:  Finding{Tool: "biome", Config: "biome.json", Status: Unknown, Hint: wantBiomeUnknown},
		},
		{
			name:  "unknown version no form",
			files: map[string]string{"biome.json": `{ "formatter": {} }`},
			want:  Finding{Tool: "biome", Config: "biome.json", Status: Unknown, Hint: wantBiomeUnknown},
		},
		{
			name:  "biome.jsonc with comments",
			files: map[string]string{"biome.jsonc": "{\n  // keep Biome out of the orchestrator files\n  " + biomeSchema("2.0.0") + ",\n  \"files\": { \"includes\": [\"**\", \"!**/.batuta\",] }\n}"},
			want:  Finding{Tool: "biome", Config: "biome.jsonc", Status: Ignored},
		},
		{
			name:  "invalid config",
			files: map[string]string{"biome.json": `{ "files": `},
			want:  Finding{Tool: "biome", Config: "biome.json", Status: Unknown, Hint: "unexpected end of JSON input"},
		},
		{
			name:  "includes not a list",
			files: map[string]string{"biome.json": `{ ` + biomeSchema("2.0.0") + `, "files": { "includes": "**" } }`},
			want:  Finding{Tool: "biome", Config: "biome.json", Status: Unknown, Hint: "files.includes must be a list of strings"},
		},
		{
			name:  "no biome config nor script",
			files: map[string]string{"package.json": `{ "scripts": { "test": "go test" } }`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := biomeFindings(t, tt.files)
			var want []Finding
			if tt.want != (Finding{}) {
				want = []Finding{tt.want}
			}
			if len(got) == 0 && len(want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("findings = %+v, want %+v", got, want)
			}
		})
	}
}

func TestCheckBiomeBothConfigs(t *testing.T) {
	t.Parallel()
	got := biomeFindings(t, map[string]string{
		"biome.json":  `{ ` + biomeSchema("2.0.0") + `, "files": { "includes": ["**", "!**/.batuta"] } }`,
		"biome.jsonc": `{ ` + biomeSchema("2.0.0") + ` }`,
	})
	want := []Finding{
		{Tool: "biome", Config: "biome.json", Status: Ignored},
		{Tool: "biome", Config: "biome.jsonc", Status: Missing, Hint: wantBiomeV2Absent},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("findings = %+v, want %+v", got, want)
	}
}

func TestCheckBiomeVersion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		files map[string]string
		want  Finding
	}{
		{
			name:  "schema url wins over package.json",
			files: map[string]string{"biome.json": `{ ` + biomeSchema("1.9.4") + ` }`, "package.json": `{ "devDependencies": { "@biomejs/biome": "^2.0.0" } }`},
			want:  Finding{Tool: "biome", Config: "biome.json", Status: Missing, Hint: wantBiomeV1},
		},
		{
			name:  "caret",
			files: map[string]string{"biome.json": `{}`, "package.json": `{ "devDependencies": { "@biomejs/biome": "^2.0.0" } }`},
			want:  Finding{Tool: "biome", Config: "biome.json", Status: Missing, Hint: wantBiomeV2Absent},
		},
		{
			name:  "tilde",
			files: map[string]string{"biome.json": `{}`, "package.json": `{ "devDependencies": { "@biomejs/biome": "~2.1" } }`},
			want:  Finding{Tool: "biome", Config: "biome.json", Status: Missing, Hint: wantBiomeV2Absent},
		},
		{
			name:  "range",
			files: map[string]string{"biome.json": `{}`, "package.json": `{ "dependencies": { "@biomejs/biome": ">=2" } }`},
			want:  Finding{Tool: "biome", Config: "biome.json", Status: Missing, Hint: wantBiomeV2Absent},
		},
		{
			name:  "pinned v1",
			files: map[string]string{"biome.json": `{}`, "package.json": `{ "devDependencies": { "@biomejs/biome": "1.9.4" } }`},
			want:  Finding{Tool: "biome", Config: "biome.json", Status: Missing, Hint: wantBiomeV1},
		},
		{
			name:  "v prefix and space",
			files: map[string]string{"biome.json": `{}`, "package.json": `{ "devDependencies": { "@biomejs/biome": "= v2.0.0" } }`},
			want:  Finding{Tool: "biome", Config: "biome.json", Status: Missing, Hint: wantBiomeV2Absent},
		},
		{
			name:  "latest",
			files: map[string]string{"biome.json": `{}`, "package.json": `{ "devDependencies": { "@biomejs/biome": "latest" } }`},
			want:  Finding{Tool: "biome", Config: "biome.json", Status: Unknown, Hint: wantBiomeUnknown},
		},
		{
			name:  "workspace",
			files: map[string]string{"biome.json": `{}`, "package.json": `{ "devDependencies": { "@biomejs/biome": "workspace:*" } }`},
			want:  Finding{Tool: "biome", Config: "biome.json", Status: Unknown, Hint: wantBiomeUnknown},
		},
		{
			name:  "catalog",
			files: map[string]string{"biome.json": `{}`, "package.json": `{ "devDependencies": { "@biomejs/biome": "catalog:" } }`},
			want:  Finding{Tool: "biome", Config: "biome.json", Status: Unknown, Hint: wantBiomeUnknown},
		},
		{
			name:  "schema without a version falls back to package.json",
			files: map[string]string{"biome.json": `{ "$schema": "./node_modules/@biomejs/biome/configuration_schema.json" }`, "package.json": `{ "devDependencies": { "@biomejs/biome": "2.0.0" } }`},
			want:  Finding{Tool: "biome", Config: "biome.json", Status: Missing, Hint: wantBiomeV2Absent},
		},
		{
			name:  "schema with a tag is unknown",
			files: map[string]string{"biome.json": `{ ` + biomeSchema("latest") + ` }`},
			want:  Finding{Tool: "biome", Config: "biome.json", Status: Unknown, Hint: wantBiomeUnknown},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := biomeFindings(t, tt.files)
			if !reflect.DeepEqual(got, []Finding{tt.want}) {
				t.Fatalf("findings = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestCheckBiomeIncludes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		includes string
		want     Finding
	}{
		{"negated", `["**", "!**/.batuta"]`, Finding{Status: Ignored}},
		{"force ignored", `["**", "!!**/.batuta"]`, Finding{Status: Ignored}},
		{"later pattern that does not match", `["**", "!**/.batuta", "src/**"]`, Finding{Status: Ignored}},
		{"positive named pattern", `["**", ".batuta/**"]`, Finding{Status: Missing, Hint: wantBiomeV2Includes}},
		{"negation before the wildcard", `["!**/.batuta", "**"]`, Finding{Status: Missing, Hint: wantBiomeV2Includes}},
		{"later wildcard re-includes", `["**", "!**/.batuta", "**/*"]`, Finding{Status: Missing, Hint: wantBiomeV2Includes}},
		{"leading dot slash", `["./**", "!./.batuta"]`, Finding{Status: Ignored}},
		{"rooted directory", `["**", "!.batuta/**"]`, Finding{Status: Ignored}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := biomeFindings(t, map[string]string{
				"biome.json": `{ ` + biomeSchema("2.0.0") + `, "files": { "includes": ` + tt.includes + ` } }`,
			})
			want := tt.want
			want.Tool, want.Config = "biome", "biome.json"
			if !reflect.DeepEqual(got, []Finding{want}) {
				t.Fatalf("findings = %+v, want %+v", got, want)
			}
		})
	}
	t.Run("absent", func(t *testing.T) {
		t.Parallel()
		got := biomeFindings(t, map[string]string{"biome.json": `{ ` + biomeSchema("2.0.0") + ` }`})
		want := []Finding{{Tool: "biome", Config: "biome.json", Status: Missing, Hint: wantBiomeV2Absent}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("findings = %+v, want %+v", got, want)
		}
	})
}

func TestCheckBiomeUnknownVersion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		config string
		want   Finding
	}{
		{"includes negated", `{ "files": { "includes": ["**", "!**/.batuta"] } }`, Finding{Status: Ignored}},
		{"files.ignore entry", `{ "files": { "ignore": [".batuta"] } }`, Finding{Status: Ignored}},
		{"includes positive", `{ "files": { "includes": ["**", ".batuta/**"] } }`, Finding{Status: Unknown, Hint: wantBiomeUnknown}},
		{"includes re-included", `{ "files": { "includes": ["**", "!**/.batuta", "**"] } }`, Finding{Status: Unknown, Hint: wantBiomeUnknown}},
		{"files.ignore without an entry", `{ "files": { "ignore": ["dist"] } }`, Finding{Status: Unknown, Hint: wantBiomeUnknown}},
		{"neither form", `{ "linter": { "enabled": true } }`, Finding{Status: Unknown, Hint: wantBiomeUnknown}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := biomeFindings(t, map[string]string{"biome.json": tt.config})
			want := tt.want
			want.Tool, want.Config = "biome", "biome.json"
			if !reflect.DeepEqual(got, []Finding{want}) {
				t.Fatalf("findings = %+v, want %+v", got, want)
			}
		})
	}
}

func TestCheckScriptOnlyBiome(t *testing.T) {
	t.Parallel()
	got := biomeFindings(t, map[string]string{
		"package.json": `{ "scripts": { "lint": "biome check ." } }`,
	})
	want := []Finding{{Tool: "biome", Config: "package.json", Key: "scripts", Status: Missing, Hint: wantBiomeScript}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("findings = %+v, want %+v", got, want)
	}
}

func TestCheckBiomeRootOnly(t *testing.T) {
	t.Parallel()
	config := `{ ` + biomeSchema("2.0.0") + ` }`
	got := biomeFindings(t, map[string]string{
		"biome.json":                config,
		"packages/a/biome.json":     config,
		"node_modules/x/biome.json": config,
	})
	want := []Finding{{Tool: "biome", Config: "biome.json", Status: Missing, Hint: wantBiomeV2Absent}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("findings = %+v, want %+v", got, want)
	}
}
