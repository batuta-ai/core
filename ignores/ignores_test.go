package ignores

import (
	"encoding/json"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func reporting(tool string, findings ...Finding) detector {
	return detector{tool: tool, run: func(string, *packageJSON) ([]Finding, error) {
		return findings, nil
	}}
}

func TestCheckNeedsBatutaDir(t *testing.T) {
	t.Parallel()
	root := tempDir(t)
	writeFile(t, root, "package.json", `{}`)
	d := reporting("vitest", Finding{Tool: "vitest", Config: "vitest.config.ts", Status: Missing})

	got, err := check(root, []detector{d})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("check without .batuta/ = %v, want no findings", got)
	}
}

func TestCheckRootErrors(t *testing.T) {
	t.Parallel()
	d := reporting("vitest", Finding{Tool: "vitest", Status: Missing})
	tests := []struct {
		name  string
		setup func(t *testing.T) string
	}{
		{name: "root missing", setup: func(t *testing.T) string {
			return filepath.Join(tempDir(t), "absent")
		}},
		{name: "root is a file", setup: func(t *testing.T) string {
			root := tempDir(t)
			writeFile(t, root, "file", "x")
			return filepath.Join(root, "file")
		}},
		{name: ".batuta is a file", setup: func(t *testing.T) string {
			root := tempDir(t)
			writeFile(t, root, ".batuta", "x")
			return root
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := check(tt.setup(t), []detector{d})
			if err == nil {
				t.Fatalf("check = %v, want an error", got)
			}
			if len(got) != 0 {
				t.Fatalf("check returned findings %v with error %v", got, err)
			}
		})
	}
}

func TestCheckSortOrder(t *testing.T) {
	t.Parallel()
	root := tempDir(t)
	if err := os.Mkdir(filepath.Join(root, ".batuta"), 0o755); err != nil {
		t.Fatal(err)
	}
	d := reporting("jest",
		Finding{Tool: "prettier", Config: "package.json", Key: "prettier", Status: Missing},
		Finding{Tool: "jest", Config: "package.json", Key: "scripts", Status: Missing},
		Finding{Tool: "jest", Config: "jest.config.js", Status: Ignored},
		Finding{Tool: "jest", Config: "package.json", Key: "jest", Status: Ignored},
		Finding{Tool: "biome", Config: "biome.json", Status: Ignored},
	)

	got, err := check(root, []detector{d})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	var order []string
	for _, f := range got {
		order = append(order, f.Tool+"|"+f.Config+"|"+f.Key)
	}
	want := []string{
		"biome|biome.json|",
		"jest|jest.config.js|",
		"jest|package.json|jest",
		"jest|package.json|scripts",
		"prettier|package.json|prettier",
	}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
}

func TestCheckInvalidPackageJSON(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		detectors int
	}{
		{name: "one detector", detectors: 1},
		{name: "three detectors", detectors: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := tempDir(t)
			if err := os.Mkdir(filepath.Join(root, ".batuta"), 0o755); err != nil {
				t.Fatal(err)
			}
			writeFile(t, root, "package.json", `{"scripts": `)
			var detectors []detector
			var received []*packageJSON
			for i := 0; i < tt.detectors; i++ {
				detectors = append(detectors, detector{tool: "vitest", run: func(_ string, pkg *packageJSON) ([]Finding, error) {
					received = append(received, pkg)
					return nil, nil
				}})
			}

			got, err := check(root, detectors)
			if err != nil {
				t.Fatalf("check: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("findings = %v, want exactly one", got)
			}
			f := got[0]
			if f.Tool != "package.json" || f.Config != "package.json" || f.Status != Unknown || f.Hint == "" {
				t.Fatalf("finding = %+v, want unknown package.json with the parse error", f)
			}
			var syntax *json.SyntaxError
			var probe any
			if jsonErr := json.Unmarshal([]byte(`{"scripts": `), &probe); !errors.As(jsonErr, &syntax) || f.Hint != jsonErr.Error() {
				t.Fatalf("hint = %q, want the parse error %v", f.Hint, jsonErr)
			}
			if len(received) != tt.detectors {
				t.Fatalf("detectors ran %d times, want %d", len(received), tt.detectors)
			}
			for _, pkg := range received {
				if pkg != nil {
					t.Fatalf("detector received %+v, want the package as absent", pkg)
				}
			}
		})
	}
}

func TestCheckPassesParsedPackageJSON(t *testing.T) {
	t.Parallel()
	root := tempDir(t)
	if err := os.Mkdir(filepath.Join(root, ".batuta"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "package.json", `{"scripts": {"test": "vitest"}, "jest": {"a": 1}}`)
	var seen *packageJSON
	d := detector{tool: "vitest", run: func(_ string, pkg *packageJSON) ([]Finding, error) {
		seen = pkg
		return nil, nil
	}}

	if _, err := check(root, []detector{d}); err != nil {
		t.Fatalf("check: %v", err)
	}
	if seen == nil || seen.Scripts["test"] != "vitest" {
		t.Fatalf("package = %+v, want scripts.test = vitest", seen)
	}
	if _, ok := seen.Raw["jest"]; !ok {
		t.Fatalf("package.Raw = %v, want the jest key", seen.Raw)
	}
}

func TestCheckDetectorError(t *testing.T) {
	t.Parallel()
	root := tempDir(t)
	if err := os.Mkdir(filepath.Join(root, ".batuta"), 0o755); err != nil {
		t.Fatal(err)
	}
	failing := detector{tool: "eslint", run: func(string, *packageJSON) ([]Finding, error) {
		return nil, errors.New("read eslint.config.js: boom")
	}}
	healthy := reporting("jest", Finding{Tool: "jest", Config: "jest.config.js", Status: Ignored})

	got, err := check(root, []detector{failing, healthy})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	want := []Finding{
		{Tool: "eslint", Status: Unknown, Hint: "read eslint.config.js: boom"},
		{Tool: "jest", Config: "jest.config.js", Status: Ignored},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("findings = %+v, want %+v", got, want)
	}
}

func TestFindingJSON(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		finding Finding
		want    string
	}{
		{
			name:    "key and hint omitted",
			finding: Finding{Tool: "jest", Config: "jest.config.js", Status: Ignored},
			want:    `{"tool":"jest","config":"jest.config.js","status":"ignored"}`,
		},
		{
			name:    "key and hint set",
			finding: Finding{Tool: "jest", Config: "package.json", Key: "jest", Status: Missing, Hint: "add it"},
			want:    `{"tool":"jest","config":"package.json","key":"jest","status":"missing","hint":"add it"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := json.Marshal(tt.finding)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(got) != tt.want {
				t.Fatalf("json = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestIgnoresNoExec(t *testing.T) {
	t.Parallel()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		checked++
		for _, imp := range file.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatalf("%s: unquote %s: %v", name, imp.Path.Value, err)
			}
			if path == "os/exec" {
				t.Errorf("%s imports os/exec", name)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no non-test .go file checked")
	}
}
