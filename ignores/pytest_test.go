package ignores

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

var pytestDetectors = []detector{{tool: "pytest", run: checkPytest}}

// pytestFindings builds a workspace with a .batuta directory and the given
// files, and returns what the pytest detector reports.
func pytestFindings(t *testing.T, files map[string]string) []Finding {
	t.Helper()
	root := tempDir(t)
	if err := os.MkdirAll(filepath.Join(root, ".batuta"), 0o755); err != nil {
		t.Fatalf("create .batuta: %v", err)
	}
	for name, content := range files {
		writeFile(t, root, name, content)
	}
	got, err := check(root, pytestDetectors)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	return got
}

const wantPytestMissing = "keep '.*' in norecursedirs, or add .batuta"

func pytestFinding(config, status, hint string) []Finding {
	return []Finding{{Tool: "pytest", Config: config, Status: status, Hint: hint}}
}

func TestCheckPytest(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		files map[string]string
		want  []Finding
	}{
		{
			name:  "pytest.ini without norecursedirs",
			files: map[string]string{"pytest.ini": "[pytest]\naddopts = -q\n"},
			want:  pytestFinding("pytest.ini", NotNeeded, ""),
		},
		{
			name:  "pytest.ini with a matching entry",
			files: map[string]string{"pytest.ini": "[pytest]\nnorecursedirs = .* build\n"},
			want:  pytestFinding("pytest.ini", Ignored, ""),
		},
		{
			name:  "pytest.ini without a match and without testpaths",
			files: map[string]string{"pytest.ini": "[pytest]\nnorecursedirs = build dist\n"},
			want:  pytestFinding("pytest.ini", Missing, wantPytestMissing),
		},
		{
			name:  "no match and named testpaths",
			files: map[string]string{"pytest.ini": "[pytest]\nnorecursedirs = build\ntestpaths = tests src/unit\n", "tests/test_a.py": ""},
			want:  pytestFinding("pytest.ini", NotNeeded, ""),
		},
		{
			name:  "no match and a testpaths entry that is the root",
			files: map[string]string{"pytest.ini": "[pytest]\nnorecursedirs = build\ntestpaths = tests .\n"},
			want:  pytestFinding("pytest.ini", Missing, wantPytestMissing),
		},
		{
			name:  "no match and a testpaths glob first segment",
			files: map[string]string{"pytest.ini": "[pytest]\nnorecursedirs = build\ntestpaths = */tests\n"},
			want:  pytestFinding("pytest.ini", Missing, wantPytestMissing),
		},
		{
			name:  "no match and a testpaths entry with a parent segment",
			files: map[string]string{"pytest.ini": "[pytest]\nnorecursedirs = build\ntestpaths = tests ../other\n"},
			want:  pytestFinding("pytest.ini", Missing, wantPytestMissing),
		},
		{
			name:  "no match and empty testpaths",
			files: map[string]string{"pytest.ini": "[pytest]\nnorecursedirs = build\ntestpaths =\n"},
			want:  pytestFinding("pytest.ini", Missing, wantPytestMissing),
		},
		{
			name:  ".pytest.ini",
			files: map[string]string{".pytest.ini": "[pytest]\nnorecursedirs = build\n"},
			want:  pytestFinding(".pytest.ini", Missing, wantPytestMissing),
		},
		{
			name:  "pytest.toml",
			files: map[string]string{"pytest.toml": "[pytest]\nnorecursedirs = [\"build\"]\n"},
			want:  pytestFinding("pytest.toml", Missing, wantPytestMissing),
		},
		{
			name:  ".pytest.toml",
			files: map[string]string{".pytest.toml": "[pytest]\nnorecursedirs = [\".*\"]\n"},
			want:  pytestFinding(".pytest.toml", Ignored, ""),
		},
		{
			name:  "pytest.toml with testpaths as a list",
			files: map[string]string{"pytest.toml": "[pytest]\nnorecursedirs = [\"build\"]\ntestpaths = [\"tests\"]\n", "tests/test_a.py": ""},
			want:  pytestFinding("pytest.toml", NotNeeded, ""),
		},
		{
			name:  "pytest.toml with testpaths as a string",
			files: map[string]string{"pytest.toml": "[pytest]\nnorecursedirs = \"build\"\ntestpaths = \"tests unit\"\n", "unit/test_a.py": ""},
			want:  pytestFinding("pytest.toml", NotNeeded, ""),
		},
		{
			name:  "pyproject.toml with tool.pytest",
			files: map[string]string{"pyproject.toml": "[tool.pytest]\nnorecursedirs = [\"build\"]\n"},
			want:  pytestFinding("pyproject.toml", Missing, wantPytestMissing),
		},
		{
			name:  "pyproject.toml with tool.pytest.ini_options",
			files: map[string]string{"pyproject.toml": "[tool.pytest.ini_options]\nnorecursedirs = [\"build\"]\n"},
			want:  pytestFinding("pyproject.toml", Missing, wantPytestMissing),
		},
		{
			name:  "pyproject.toml with tool.pytest.ini_options and a match",
			files: map[string]string{"pyproject.toml": "[tool.pytest.ini_options]\nnorecursedirs = [\"*.batuta\"]\n"},
			want:  pytestFinding("pyproject.toml", Ignored, ""),
		},
		{
			name:  "tox.ini",
			files: map[string]string{"tox.ini": "[tox]\nenvlist = py\n[pytest]\nnorecursedirs = build\n"},
			want:  pytestFinding("tox.ini", Missing, wantPytestMissing),
		},
		{
			name:  "setup.cfg",
			files: map[string]string{"setup.cfg": "[metadata]\nname = x\n[tool:pytest]\nnorecursedirs = .*\n"},
			want:  pytestFinding("setup.cfg", Ignored, ""),
		},
		{
			name:  "setup.cfg with a colon separator",
			files: map[string]string{"setup.cfg": "[tool:pytest]\nnorecursedirs: build\n"},
			want:  pytestFinding("setup.cfg", Missing, wantPytestMissing),
		},
		{
			name:  "ini comments are skipped",
			files: map[string]string{"pytest.ini": "[pytest]\n# norecursedirs = .*\n; norecursedirs = .*\nnorecursedirs = build\n"},
			want:  pytestFinding("pytest.ini", Missing, wantPytestMissing),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := pytestFindings(t, tt.files)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestCheckPytestPrecedence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		files map[string]string
		want  []Finding
	}{
		{
			name:  "empty pytest.toml beats pytest.ini",
			files: map[string]string{"pytest.toml": "", "pytest.ini": "[pytest]\nnorecursedirs = build\n"},
			want:  pytestFinding("pytest.toml", NotNeeded, ""),
		},
		{
			name:  "pytest.toml beats .pytest.toml",
			files: map[string]string{"pytest.toml": "[pytest]\nnorecursedirs = [\"build\"]\n", ".pytest.toml": "[pytest]\nnorecursedirs = [\".*\"]\n"},
			want:  pytestFinding("pytest.toml", Missing, wantPytestMissing),
		},
		{
			name:  ".pytest.toml beats pytest.ini",
			files: map[string]string{".pytest.toml": "", "pytest.ini": "[pytest]\nnorecursedirs = build\n"},
			want:  pytestFinding(".pytest.toml", NotNeeded, ""),
		},
		{
			name:  "empty pytest.ini beats pyproject.toml",
			files: map[string]string{"pytest.ini": "", "pyproject.toml": "[tool.pytest]\nnorecursedirs = [\"build\"]\n"},
			want:  pytestFinding("pytest.ini", NotNeeded, ""),
		},
		{
			name:  "sectionless pytest.ini beats pyproject.toml",
			files: map[string]string{"pytest.ini": "# nothing\n", "pyproject.toml": "[tool.pytest]\nnorecursedirs = [\"build\"]\n"},
			want:  pytestFinding("pytest.ini", NotNeeded, ""),
		},
		{
			name:  ".pytest.ini beats pyproject.toml",
			files: map[string]string{".pytest.ini": "", "pyproject.toml": "[tool.pytest]\nnorecursedirs = [\"build\"]\n"},
			want:  pytestFinding(".pytest.ini", NotNeeded, ""),
		},
		{
			name:  "pyproject.toml without a pytest table yields to tox.ini",
			files: map[string]string{"pyproject.toml": "[project]\nname = \"x\"\n", "tox.ini": "[pytest]\nnorecursedirs = build\n"},
			want:  pytestFinding("tox.ini", Missing, wantPytestMissing),
		},
		{
			name:  "pyproject.toml beats tox.ini",
			files: map[string]string{"pyproject.toml": "[tool.pytest]\nnorecursedirs = [\".*\"]\n", "tox.ini": "[pytest]\nnorecursedirs = build\n"},
			want:  pytestFinding("pyproject.toml", Ignored, ""),
		},
		{
			name:  "tox.ini without a pytest section yields to setup.cfg",
			files: map[string]string{"tox.ini": "[tox]\nenvlist = py\n", "setup.cfg": "[tool:pytest]\nnorecursedirs = build\n"},
			want:  pytestFinding("setup.cfg", Missing, wantPytestMissing),
		},
		{
			name:  "tox.ini beats setup.cfg",
			files: map[string]string{"tox.ini": "[pytest]\nnorecursedirs = .*\n", "setup.cfg": "[tool:pytest]\nnorecursedirs = build\n"},
			want:  pytestFinding("tox.ini", Ignored, ""),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := pytestFindings(t, tt.files)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestCheckPytestNorecursedirs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		file   string
		config string
		want   string
	}{
		{"ini dot star and build", "pytest.ini", "[pytest]\nnorecursedirs = .* build\n", Ignored},
		{"ini continued on indented lines", "pytest.ini", "[pytest]\nnorecursedirs = build\n    dist\n    .*\naddopts = -q\n", Ignored},
		{"ini continuation without a match", "pytest.ini", "[pytest]\nnorecursedirs =\n    build\n    dist\n", Missing},
		{"ini quoted entry", "pytest.ini", "[pytest]\nnorecursedirs = \".*\" build\n", Ignored},
		{"ini single quoted entry", "pytest.ini", "[pytest]\nnorecursedirs = '.*'\n", Ignored},
		{"ini suffix glob", "pytest.ini", "[pytest]\nnorecursedirs = *.batuta\n", Ignored},
		{"ini prefix glob", "pytest.ini", "[pytest]\nnorecursedirs = .b*\n", Ignored},
		{"ini exact name", "pytest.ini", "[pytest]\nnorecursedirs = .batuta\n", Ignored},
		{"ini look-alike name", "pytest.ini", "[pytest]\nnorecursedirs = .batuta-old\n", Missing},
		{"ini build alone", "pytest.ini", "[pytest]\nnorecursedirs = build\n", Missing},
		{"toml list", "pytest.toml", "[pytest]\nnorecursedirs = [\".*\", \"build\"]\n", Ignored},
		{"toml list without a match", "pytest.toml", "[pytest]\nnorecursedirs = [\"build\", \".batuta-old\"]\n", Missing},
		{"toml whitespace split string", "pytest.toml", "[pytest]\nnorecursedirs = \"build .*\"\n", Ignored},
		{"toml suffix glob", "pyproject.toml", "[tool.pytest]\nnorecursedirs = [\"*.batuta\"]\n", Ignored},
		{"toml prefix glob", "pyproject.toml", "[tool.pytest.ini_options]\nnorecursedirs = [\".b*\"]\n", Ignored},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := pytestFindings(t, map[string]string{tt.file: tt.config})
			if len(got) != 1 || got[0].Status != tt.want {
				t.Fatalf("got %+v, want one %s finding", got, tt.want)
			}
		})
	}
}

func TestCheckPytestAbsent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		files map[string]string
	}{
		{"nothing", map[string]string{}},
		{"pyproject.toml without a pytest table", map[string]string{"pyproject.toml": "[project]\nname = \"x\"\n"}},
		{"tox.ini without a pytest section", map[string]string{"tox.ini": "[tox]\nenvlist = py\n"}},
		{"setup.cfg without a pytest section", map[string]string{"setup.cfg": "[metadata]\nname = x\n"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := pytestFindings(t, tt.files); len(got) != 0 {
				t.Fatalf("got %+v, want no finding", got)
			}
		})
	}
}

func TestCheckPytestRootOnly(t *testing.T) {
	t.Parallel()
	got := pytestFindings(t, map[string]string{
		"pytest.ini":            "[pytest]\nnorecursedirs = build\n",
		"packages/a/pytest.ini": "[pytest]\nnorecursedirs = build\n",
	})
	want := pytestFinding("pytest.ini", Missing, wantPytestMissing)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestCheckPytestInvalid(t *testing.T) {
	t.Parallel()
	got := pytestFindings(t, map[string]string{"pytest.toml": "[pytest\n"})
	if len(got) != 1 || got[0].Tool != "pytest" || got[0].Config != "pytest.toml" || got[0].Status != Unknown || got[0].Hint == "" {
		t.Fatalf("got %+v, want one unknown finding for pytest.toml carrying the parse error", got)
	}
}

func TestCheckPytestTestpaths(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"batuta entry", map[string]string{"pytest.ini": "[pytest]\nnorecursedirs = build\ntestpaths = .batuta\n"}, Missing},
		{"dot slash batuta entry", map[string]string{"pytest.ini": "[pytest]\nnorecursedirs = build\ntestpaths = ./.batuta/x\n", "tests/test_a.py": ""}, Missing},
		{"batuta beside an existing directory", map[string]string{"pytest.ini": "[pytest]\nnorecursedirs = build\ntestpaths = tests .batuta\n", "tests/test_a.py": ""}, Missing},
		{"directory that does not exist", map[string]string{"pytest.ini": "[pytest]\nnorecursedirs = build\ntestpaths = tests\n"}, Missing},
		{"entry that is a file", map[string]string{"pytest.ini": "[pytest]\nnorecursedirs = build\ntestpaths = tests\n", "tests": ""}, Missing},
		{"directory that exists", map[string]string{"pytest.ini": "[pytest]\nnorecursedirs = build\ntestpaths = tests\n", "tests/test_a.py": ""}, NotNeeded},
		{"one of two exists", map[string]string{"pytest.ini": "[pytest]\nnorecursedirs = build\ntestpaths = tests missing\n", "tests/test_a.py": ""}, NotNeeded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := pytestFindings(t, tt.files)
			if len(got) != 1 || got[0].Status != tt.want {
				t.Fatalf("got %+v, want one %s finding", got, tt.want)
			}
		})
	}
}
