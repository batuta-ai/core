// Package ignores reports, for each test runner, linter, formatter and type
// checker configured at a workspace root, whether it keeps out of `.batuta/`.
// It reads files and never evaluates code or edits anything.
package ignores

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Statuses a finding can carry; `batuta doctor` prints the same strings.
const (
	Ignored   = "ignored"
	NotNeeded = "not_needed"
	Missing   = "missing"
	Unknown   = "unknown"
)

// Tools lists every tool a detector covers.
var Tools = []string{"biome", "eslint", "jest", "playwright", "prettier", "pytest", "typescript", "vitest"}

// Finding is one tool's verdict. Key names the package.json key it came from.
type Finding struct {
	Tool   string `json:"tool"`
	Config string `json:"config"`
	Key    string `json:"key,omitempty"`
	Status string `json:"status"`
	Hint   string `json:"hint,omitempty"`
}

// packageJSON is the parsed root package.json; Raw keeps every top-level key.
type packageJSON struct {
	Scripts         map[string]string          `json:"scripts"`
	Dependencies    map[string]string          `json:"dependencies"`
	DevDependencies map[string]string          `json:"devDependencies"`
	Raw             map[string]json.RawMessage `json:"-"`
}

type detector struct {
	tool string
	run  func(root string, pkg *packageJSON) ([]Finding, error)
}

var defaultDetectors = []detector{
	{tool: "vitest", run: checkVitest},
	{tool: "jest", run: checkJest},
	{tool: "playwright", run: checkPlaywright},
	{tool: "eslint", run: checkESLint},
	{tool: "prettier", run: checkPrettier},
	{tool: "biome", run: checkBiome},
	{tool: "typescript", run: checkTypeScript},
	{tool: "pytest", run: checkPytest},
}

// Check reports the findings for the workspace at root. A root without a
// `.batuta/` directory has nothing to protect and yields none.
func Check(root string) ([]Finding, error) {
	return check(root, defaultDetectors)
}

func check(root string, detectors []detector) ([]Finding, error) {
	if _, err := os.ReadDir(root); err != nil {
		return nil, err
	}
	batuta := filepath.Join(root, ".batuta")
	info, err := os.Lstat(batuta)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err == nil && info.Mode()&fs.ModeSymlink != 0 {
		info, err = os.Stat(batuta)
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", batuta)
	}

	findings := []Finding{}
	pkg, pkgFinding := readPackageJSON(root)
	if pkgFinding != nil {
		findings = append(findings, *pkgFinding)
	}
	for _, d := range detectors {
		got, err := d.run(root, pkg)
		if err != nil {
			findings = append(findings, Finding{Tool: d.tool, Status: Unknown, Hint: err.Error()})
			continue
		}
		findings = append(findings, got...)
	}
	sort.SliceStable(findings, func(i, j int) bool {
		a, b := findings[i], findings[j]
		if a.Tool != b.Tool {
			return a.Tool < b.Tool
		}
		if a.Config != b.Config {
			return a.Config < b.Config
		}
		return a.Key < b.Key
	})
	return findings, nil
}

// hasBatutaSegment reports whether a path, glob or pattern names `.batuta` as
// a whole segment: backslashes drop out and the rest splits on "/".
func hasBatutaSegment(s string) bool {
	for _, seg := range strings.Split(strings.ReplaceAll(s, `\`, ""), "/") {
		if seg == ".batuta" {
			return true
		}
	}
	return false
}

// readPackageJSON returns the parsed file, nil when it is missing or invalid.
// An unreadable or invalid file also yields the finding that reports it.
func readPackageJSON(root string) (*packageJSON, *Finding) {
	data, err := os.ReadFile(filepath.Join(root, "package.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, &Finding{Tool: "package.json", Config: "package.json", Status: Unknown, Hint: err.Error()}
	}
	pkg := &packageJSON{}
	if err := json.Unmarshal(data, pkg); err != nil {
		return nil, &Finding{Tool: "package.json", Config: "package.json", Status: Unknown, Hint: err.Error()}
	}
	if err := json.Unmarshal(data, &pkg.Raw); err != nil {
		return nil, &Finding{Tool: "package.json", Config: "package.json", Status: Unknown, Hint: err.Error()}
	}
	return pkg, nil
}
