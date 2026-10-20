package ignores

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var tsDetectors = []detector{{tool: "typescript", run: checkTypeScript}}

const (
	wantTSFiles   = "remove .batuta from files: exclude does not apply to files"
	wantTSInclude = `remove .batuta from include, or add ".batuta" to exclude`
)

// tsWorkspace builds a workspace with a .batuta directory and the given files.
func tsWorkspace(t *testing.T, files map[string]string) string {
	t.Helper()
	root := tempDir(t)
	if err := os.MkdirAll(filepath.Join(root, ".batuta"), 0o755); err != nil {
		t.Fatalf("create .batuta: %v", err)
	}
	for name, content := range files {
		writeFile(t, root, name, content)
	}
	return root
}

func tsCheck(t *testing.T, root string) []Finding {
	t.Helper()
	got, err := check(root, tsDetectors)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	return got
}

func tsFindings(t *testing.T, files map[string]string) []Finding {
	t.Helper()
	return tsCheck(t, tsWorkspace(t, files))
}

func tsWant(status, hint string) []Finding {
	return []Finding{{Tool: "typescript", Config: "tsconfig.json", Status: status, Hint: hint}}
}

func TestCheckTypeScript(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		config string
		want   []Finding
	}{
		{"no include", `{}`, tsWant(NotNeeded, "")},
		{"wildcard include", `{ "include": ["./**/*"] }`, tsWant(NotNeeded, "")},
		{"src include", `{ "include": ["src"] }`, tsWant(NotNeeded, "")},
		{"include names .batuta", `{ "include": [".batuta/**/*", "src"] }`, tsWant(Missing, wantTSInclude)},
		{"include names .batuta with exclude", `{ "include": [".batuta/**/*", "src"], "exclude": [".batuta"] }`, tsWant(Ignored, "")},
		{"exclude with glob suffix", `{ "include": [".batuta"], "exclude": ["./.batuta/**/*"] }`, tsWant(Ignored, "")},
		{"exclude with double star", `{ "include": [".batuta"], "exclude": ["**/.batuta"] }`, tsWant(Ignored, "")},
		{"exclude naming other path", `{ "include": [".batuta"], "exclude": ["dist"] }`, tsWant(Missing, wantTSInclude)},
		{"files entry", `{ "files": [".batuta/x.ts"] }`, tsWant(Missing, wantTSFiles)},
		{"files entry with exclude", `{ "files": [".batuta/x.ts"], "exclude": [".batuta"] }`, tsWant(Missing, wantTSFiles)},
		{"comments and trailing commas", "{ // note\n \"include\": [\".batuta/**/*\",], /* x */ \"exclude\": [\".batuta\"] }", tsWant(Ignored, "")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tsFindings(t, map[string]string{"tsconfig.json": tt.config})
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestCheckTypeScriptSegments(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		config string
		want   []Finding
	}{
		{"include of .batuta-old", `{ "include": [".batuta-old/**/*"] }`, tsWant(NotNeeded, "")},
		{"double star .batuta-old", `{ "include": ["**/.batuta-old"] }`, tsWant(NotNeeded, "")},
		{"files of .batuta-old", `{ "files": [".batuta-old/x.ts"] }`, tsWant(NotNeeded, "")},
		{"exclude of .batuta-old", `{ "include": [".batuta"], "exclude": [".batuta-old"] }`, tsWant(Missing, wantTSInclude)},
		{"exclude of double star .batuta-old", `{ "include": [".batuta"], "exclude": ["**/.batuta-old/**"] }`, tsWant(Missing, wantTSInclude)},
		{"include double star .batuta", `{ "include": ["**/.batuta/**/*"] }`, tsWant(Missing, wantTSInclude)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tsFindings(t, map[string]string{"tsconfig.json": tt.config})
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestCheckTypeScriptExtends(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		files map[string]string
		want  []Finding
	}{
		{
			name: "string without suffix",
			files: map[string]string{
				"tsconfig.json":      `{ "extends": "./tsconfig.base" }`,
				"tsconfig.base.json": `{ "include": [".batuta/**/*"], "exclude": [".batuta"] }`,
			},
			want: tsWant(Ignored, ""),
		},
		{
			name: "string with suffix",
			files: map[string]string{
				"tsconfig.json":      `{ "extends": "./tsconfig.base.json" }`,
				"tsconfig.base.json": `{ "include": [".batuta/**/*"] }`,
			},
			want: tsWant(Missing, wantTSInclude),
		},
		{
			name: "array, later entry wins",
			files: map[string]string{
				"tsconfig.json": `{ "extends": ["./a", "./b.json"] }`,
				"a.json":        `{ "include": [".batuta/**/*"] }`,
				"b.json":        `{ "include": ["src"] }`,
			},
			want: tsWant(NotNeeded, ""),
		},
		{
			name: "array, earlier entry fills what the later one leaves unset",
			files: map[string]string{
				"tsconfig.json": `{ "extends": ["./a", "./b"] }`,
				"a.json":        `{ "include": [".batuta/**/*"] }`,
				"b.json":        `{ "exclude": [".batuta"] }`,
			},
			want: tsWant(Ignored, ""),
		},
		{
			name: "own field beats the base",
			files: map[string]string{
				"tsconfig.json": `{ "extends": "./base", "include": ["src"] }`,
				"base.json":     `{ "include": [".batuta/**/*"] }`,
			},
			want: tsWant(NotNeeded, ""),
		},
		{
			name: "entries resolve against the config that sets them",
			files: map[string]string{
				"tsconfig.json":     `{ "extends": "./configs/base.json" }`,
				"configs/base.json": `{ "include": ["../.batuta/**/*"], "exclude": [".batuta"] }`,
			},
			want: tsWant(Missing, wantTSInclude),
		},
		{
			name: "exclude in the base names its own directory",
			files: map[string]string{
				"tsconfig.json":     `{ "extends": "./configs/base.json" }`,
				"configs/base.json": `{ "include": ["../.batuta/**/*"], "exclude": ["../.batuta"] }`,
			},
			want: tsWant(Ignored, ""),
		},
		{
			name: "nested extends",
			files: map[string]string{
				"tsconfig.json":  `{ "extends": "./a" }`,
				"a.json":         `{ "extends": "./configs/b" }`,
				"configs/b.json": `{ "include": ["../.batuta"], "exclude": ["../.batuta"] }`,
			},
			want: tsWant(Ignored, ""),
		},
		{
			name: "five files in all",
			files: map[string]string{
				"tsconfig.json": `{ "extends": "./1" }`,
				"1.json":        `{ "extends": "./2" }`,
				"2.json":        `{ "extends": "./3" }`,
				"3.json":        `{ "extends": "./4" }`,
				"4.json":        `{ "include": [".batuta"] }`,
			},
			want: tsWant(Missing, wantTSInclude),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tsFindings(t, tt.files)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestCheckTypeScriptExtendsLimits(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		prepare func(t *testing.T) string
		wantErr string
	}{
		{
			name: "loop",
			prepare: func(t *testing.T) string {
				return tsWorkspace(t, map[string]string{
					"tsconfig.json": `{ "extends": "./a" }`,
					"a.json":        `{ "extends": "./tsconfig.json" }`,
				})
			},
			wantErr: "loops back",
		},
		{
			name: "self reference",
			prepare: func(t *testing.T) string {
				return tsWorkspace(t, map[string]string{"tsconfig.json": `{ "extends": "./tsconfig.json" }`})
			},
			wantErr: "loops back",
		},
		{
			name: "six files in all",
			prepare: func(t *testing.T) string {
				return tsWorkspace(t, map[string]string{
					"tsconfig.json": `{ "extends": "./1" }`,
					"1.json":        `{ "extends": "./2" }`,
					"2.json":        `{ "extends": "./3" }`,
					"3.json":        `{ "extends": "./4" }`,
					"4.json":        `{ "extends": "./5" }`,
					"5.json":        `{ "include": ["src"] }`,
				})
			},
			wantErr: "longer than 5 files",
		},
		{
			name: "outside the root",
			prepare: func(t *testing.T) string {
				parent := tempDir(t)
				writeFile(t, parent, "outside.json", `{ "include": ["src"] }`)
				root := filepath.Join(parent, "root")
				writeFile(t, root, "tsconfig.json", `{ "extends": "../outside" }`)
				if err := os.MkdirAll(filepath.Join(root, ".batuta"), 0o755); err != nil {
					t.Fatalf("create .batuta: %v", err)
				}
				return root
			},
			wantErr: "outside the workspace root",
		},
		{
			name: "symlink to outside the root",
			prepare: func(t *testing.T) string {
				parent := tempDir(t)
				writeFile(t, parent, "outside/base.json", `{ "include": ["src"] }`)
				root := tsWorkspace(t, map[string]string{"tsconfig.json": `{ "extends": "./link.json" }`})
				symlink(t, filepath.Join(parent, "outside", "base.json"), filepath.Join(root, "link.json"))
				return root
			},
			wantErr: "outside the workspace root",
		},
		{
			name: "symlinked directory to outside the root",
			prepare: func(t *testing.T) string {
				parent := tempDir(t)
				writeFile(t, parent, "outside/base.json", `{ "include": ["src"] }`)
				root := tsWorkspace(t, map[string]string{"tsconfig.json": `{ "extends": "./shared/base" }`})
				symlink(t, filepath.Join(parent, "outside"), filepath.Join(root, "shared"))
				return root
			},
			wantErr: "outside the workspace root",
		},
		{
			name: "missing base",
			prepare: func(t *testing.T) string {
				return tsWorkspace(t, map[string]string{"tsconfig.json": `{ "extends": "./nope" }`})
			},
			wantErr: "nope",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tsCheck(t, tt.prepare(t))
			if len(got) != 1 || got[0].Status != Unknown || !strings.Contains(got[0].Hint, tt.wantErr) {
				t.Fatalf("got %+v, want one unknown finding mentioning %q", got, tt.wantErr)
			}
		})
	}

	t.Run("symlink inside the root is followed", func(t *testing.T) {
		t.Parallel()
		root := tsWorkspace(t, map[string]string{
			"tsconfig.json":  `{ "extends": "./link.json" }`,
			"real/base.json": `{ "include": [".batuta"] }`,
		})
		symlink(t, filepath.Join(root, "real", "base.json"), filepath.Join(root, "link.json"))
		got := tsCheck(t, root)
		if want := tsWant(Missing, wantTSInclude); !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	})

	t.Run("package extends is not read", func(t *testing.T) {
		t.Parallel()
		got := tsFindings(t, map[string]string{
			"tsconfig.json": `{ "extends": ["@tsconfig/node20/tsconfig.json", "tsconfig-base"], "include": [".batuta"], "exclude": [".batuta"] }`,
		})
		if want := tsWant(Ignored, ""); !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	})
}

func TestCheckTypeScriptFiles(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		files   map[string]string
		want    []Finding
		wantErr string
	}{
		{
			name:    "does not parse",
			files:   map[string]string{"tsconfig.json": `{ "include": [`},
			wantErr: "unexpected end of JSON input",
		},
		{
			name:    "not an object",
			files:   map[string]string{"tsconfig.json": `[]`},
			wantErr: "must be an object",
		},
		{
			name:    "include is not a list",
			files:   map[string]string{"tsconfig.json": `{ "include": "src" }`},
			wantErr: "include must be a list of strings",
		},
		{
			name:    "extends is not a string",
			files:   map[string]string{"tsconfig.json": `{ "extends": 3 }`},
			wantErr: "extends must be a string or a list of strings",
		},
		{
			name:  "variant alone gives no finding",
			files: map[string]string{"tsconfig.app.json": `{ "include": [".batuta"] }`},
			want:  []Finding{},
		},
		{
			name:  "variant beside the root config is ignored",
			files: map[string]string{"tsconfig.json": `{}`, "tsconfig.app.json": `{ "include": [".batuta"] }`},
			want:  tsWant(NotNeeded, ""),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tsFindings(t, tt.files)
			if tt.wantErr != "" {
				if len(got) != 1 || got[0].Status != Unknown || got[0].Config != "tsconfig.json" || !strings.Contains(got[0].Hint, tt.wantErr) {
					t.Fatalf("got %+v, want one unknown finding mentioning %q", got, tt.wantErr)
				}
				return
			}
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestCheckTypeScriptRootOnly(t *testing.T) {
	t.Parallel()
	config := `{ "include": [".batuta/**/*"] }`
	got := tsFindings(t, map[string]string{
		"tsconfig.json":            config,
		"packages/a/tsconfig.json": config,
	})
	if want := tsWant(Missing, wantTSInclude); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestCheckTypeScriptExtendsFiles(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		local string
		base  string
		want  []Finding
	}{
		{"base files reach .batuta from configs", `{ "extends": "./configs/base.json" }`, `{ "files": ["../.batuta/x.ts"] }`, tsWant(Missing, wantTSFiles)},
		{"base files stay inside configs", `{ "extends": "./configs/base.json" }`, `{ "files": [".batuta/x.ts"] }`, tsWant(NotNeeded, "")},
		{"local files override base files", `{ "extends": "./configs/base.json", "files": ["src/a.ts"] }`, `{ "files": ["../.batuta/x.ts"] }`, tsWant(NotNeeded, "")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tsFindings(t, map[string]string{"tsconfig.json": tt.local, "configs/base.json": tt.base})
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}
