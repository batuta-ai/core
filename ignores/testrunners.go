package ignores

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

var jsExts = []string{".js", ".mjs", ".cjs", ".ts", ".mts", ".cts"}

const (
	vitestUnknownHint = ".batuta appears outside test.exclude: check that it is excluded, not included"
	vitestMissingHint = "add '**/.batuta/**' to test.exclude, keeping the defaults: exclude: [...configDefaults.exclude, '**/.batuta/**'] (configDefaults from vitest/config)"
	vitestScriptHint  = "create vitest.config.ts with test.exclude: [...configDefaults.exclude, '**/.batuta/**']"

	jestTestHint    = "add '<rootDir>/.batuta/' to testPathIgnorePatterns (keep '/node_modules/')"
	jestModuleHint  = "add '<rootDir>/.batuta/' to modulePathIgnorePatterns"
	jestUnknownHint = ".batuta appears outside testPathIgnorePatterns and modulePathIgnorePatterns: check that both ignore '<rootDir>/.batuta/'"
	jestBothHint    = "add '<rootDir>/.batuta/' to testPathIgnorePatterns (keep '/node_modules/') and to modulePathIgnorePatterns"

	playwrightNamedHint    = "testDir names .batuta: point it at the tests directory"
	playwrightComputedHint = "testDir is computed: check that it is not the repository root, or add '**/.batuta/**' to testIgnore"
	playwrightUnknownHint  = ".batuta appears outside testIgnore: check that the tests skip it"
	playwrightMissingHint  = "set testDir to the tests directory, or add '**/.batuta/**' to testIgnore"
	playwrightScriptHint   = "create playwright.config.ts with testDir set to the tests directory"
)

// configFiles lists the files root holds that are named base plus one of exts.
func configFiles(root, base string, exts []string) []string {
	var names []string
	for _, ext := range exts {
		if _, err := os.Stat(filepath.Join(root, base+ext)); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		names = append(names, base+ext)
	}
	return names
}

func hasBatuta(literals []string) bool {
	for _, s := range literals {
		if strings.Contains(s, ".batuta") {
			return true
		}
	}
	return false
}

// keyedBatuta reports whether a literal naming .batuta sits in the value of a
// key called name.
func keyedBatuta(src, name string) (bool, error) {
	occurrences, err := keyLiterals(src, name)
	if err != nil {
		return false, err
	}
	for _, o := range occurrences {
		if hasBatuta(o.Literals) {
			return true, nil
		}
	}
	return false, nil
}

func readSource(root, name string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, name))
	return string(data), err
}

func scriptFinding(tool, hint string) Finding {
	return Finding{Tool: tool, Config: "package.json", Key: "scripts", Status: Missing, Hint: hint}
}

func sourceFinding(tool, config string, verdict func(src string) (status, hint string, err error), root string) Finding {
	f := Finding{Tool: tool, Config: config}
	src, err := readSource(root, config)
	if err == nil {
		f.Status, f.Hint, err = verdict(src)
	}
	if err != nil {
		f.Status, f.Hint = Unknown, err.Error()
	}
	return f
}

func checkVitest(root string, pkg *packageJSON) ([]Finding, error) {
	var findings []Finding
	for _, name := range configFiles(root, "vitest.config", jsExts) {
		findings = append(findings, sourceFinding("vitest", name, vitestVerdict, root))
	}
	if len(findings) == 0 {
		for _, name := range configFiles(root, "vite.config", jsExts) {
			src, err := readSource(root, name)
			if err == nil {
				var vitest bool
				if vitest, err = isVitestConfig(src); err == nil && !vitest {
					continue
				}
			}
			findings = append(findings, sourceFinding("vitest", name, vitestVerdict, root))
		}
	}
	if len(findings) == 0 && scriptTools(pkg)["vitest"] {
		findings = append(findings, scriptFinding("vitest", vitestScriptHint))
	}
	return findings, nil
}

// isVitestConfig reports whether a vite config belongs to Vitest: it names the
// vitest package or sets a test key.
func isVitestConfig(src string) (bool, error) {
	literals, err := stringLiterals(src)
	if err != nil {
		return false, err
	}
	for _, s := range literals {
		if s == "vitest" || strings.HasPrefix(s, "vitest/") {
			return true, nil
		}
	}
	tests, err := keyLiterals(src, "test")
	return len(tests) > 0, err
}

func vitestVerdict(src string) (string, string, error) {
	excluded, err := keyedBatuta(src, "exclude")
	if err != nil {
		return "", "", err
	}
	if excluded {
		return Ignored, "", nil
	}
	literals, err := stringLiterals(src)
	if err != nil {
		return "", "", err
	}
	if hasBatuta(literals) {
		return Unknown, vitestUnknownHint, nil
	}
	return Missing, vitestMissingHint, nil
}

func checkJest(root string, pkg *packageJSON) ([]Finding, error) {
	var findings []Finding
	exts := append(append([]string{}, jsExts...), ".json")
	for _, name := range configFiles(root, "jest.config", exts) {
		verdict := jestSourceVerdict
		if strings.HasSuffix(name, ".json") {
			verdict = jestJSONVerdict
		}
		findings = append(findings, sourceFinding("jest", name, verdict, root))
	}
	if pkg != nil {
		if raw, ok := pkg.Raw["jest"]; ok {
			f := Finding{Tool: "jest", Config: "package.json", Key: "jest"}
			f.Status, f.Hint = jestJSONRawVerdict(raw)
			findings = append(findings, f)
		}
	}
	if len(findings) == 0 && scriptTools(pkg)["jest"] {
		findings = append(findings, scriptFinding("jest", jestBothHint))
	}
	return findings, nil
}

func jestVerdict(testKeyed, moduleKeyed, stray bool) (string, string) {
	switch {
	case testKeyed && moduleKeyed:
		return Ignored, ""
	case moduleKeyed:
		return Missing, jestTestHint
	case testKeyed:
		return Missing, jestModuleHint
	case stray:
		return Unknown, jestUnknownHint
	}
	return Missing, jestBothHint
}

func jestSourceVerdict(src string) (string, string, error) {
	testKeyed, err := keyedBatuta(src, "testPathIgnorePatterns")
	if err != nil {
		return "", "", err
	}
	moduleKeyed, err := keyedBatuta(src, "modulePathIgnorePatterns")
	if err != nil {
		return "", "", err
	}
	literals, err := stringLiterals(src)
	if err != nil {
		return "", "", err
	}
	status, hint := jestVerdict(testKeyed, moduleKeyed, hasBatuta(literals))
	return status, hint, nil
}

func jestJSONVerdict(src string) (string, string, error) {
	value, err := parseJSONC([]byte(src))
	if err != nil {
		return "", "", err
	}
	status, hint := jestValueVerdict(value)
	return status, hint, nil
}

func jestJSONRawVerdict(raw json.RawMessage) (string, string) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return Unknown, err.Error()
	}
	return jestValueVerdict(value)
}

func jestValueVerdict(value any) (string, string) {
	object, _ := value.(map[string]any)
	return jestVerdict(
		arrayHasBatuta(object["testPathIgnorePatterns"]),
		arrayHasBatuta(object["modulePathIgnorePatterns"]),
		valueHasBatuta(value),
	)
}

func arrayHasBatuta(value any) bool {
	array, ok := value.([]any)
	return ok && valueHasBatuta(array)
}

// valueHasBatuta reports whether a string containing .batuta sits anywhere in
// a decoded JSON value.
func valueHasBatuta(value any) bool {
	switch v := value.(type) {
	case string:
		return strings.Contains(v, ".batuta")
	case []any:
		for _, e := range v {
			if valueHasBatuta(e) {
				return true
			}
		}
	case map[string]any:
		for _, e := range v {
			if valueHasBatuta(e) {
				return true
			}
		}
	}
	return false
}

func checkPlaywright(root string, pkg *packageJSON) ([]Finding, error) {
	var findings []Finding
	for _, name := range configFiles(root, "playwright.config", jsExts) {
		findings = append(findings, sourceFinding("playwright", name, playwrightVerdict, root))
	}
	if len(findings) == 0 && scriptTools(pkg)["playwright"] {
		findings = append(findings, scriptFinding("playwright", playwrightScriptHint))
	}
	return findings, nil
}

func playwrightVerdict(src string) (string, string, error) {
	value, literal, found, err := keyValue(src, "testDir")
	if err != nil {
		return "", "", err
	}
	if found && !literal {
		return Unknown, playwrightComputedHint, nil
	}
	if first, ok := namedDirectory(value); found && ok {
		if first == ".batuta" {
			return Missing, playwrightNamedHint, nil
		}
		return NotNeeded, "", nil
	}
	ignored, err := keyedBatuta(src, "testIgnore")
	if err != nil {
		return "", "", err
	}
	if ignored {
		return Ignored, "", nil
	}
	literals, err := stringLiterals(src)
	if err != nil {
		return "", "", err
	}
	if hasBatuta(literals) {
		return Unknown, playwrightUnknownHint, nil
	}
	return Missing, playwrightMissingHint, nil
}

// namedDirectory returns the first segment of a relative directory that stays
// inside the root and names one directory; it reports false for the root
// itself, an absolute path, a path with a ".." segment and a glob first segment.
func namedDirectory(dir string) (string, bool) {
	dir = strings.TrimPrefix(dir, "./")
	if dir == "" || dir == "." || path.IsAbs(dir) || filepath.IsAbs(dir) {
		return "", false
	}
	segments := strings.Split(dir, "/")
	for _, s := range segments {
		if s == ".." {
			return "", false
		}
	}
	if strings.Contains(segments[0], "*") {
		return "", false
	}
	return segments[0], true
}
