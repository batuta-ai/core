package ignores

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	eslintUnknownHint = ".batuta is not in a global ignore: add { ignores: ['.batuta/**'] } as its own element of the config array"
	eslintMissingHint = "add { ignores: ['.batuta/**'] } as its own element of the config array"
	eslintLegacyHint  = "add .batuta/ to .eslintignore"
	eslintScriptHint  = "create eslint.config.js with { ignores: ['.batuta/**'] } as its own element of the config array"

	prettierMissingHint = "add .batuta/ to .prettierignore"
)

var (
	eslintLegacyNames = []string{".eslintrc", ".eslintrc.json", ".eslintrc.js", ".eslintrc.cjs", ".eslintrc.yaml", ".eslintrc.yml"}
	prettierRCExts    = []string{"", ".json", ".yaml", ".yml", ".json5", ".js", ".cjs", ".mjs", ".ts", ".cts", ".mts", ".toml"}
)

// ignoreFileMatches reports whether a line of an ignore file names .batuta.
func ignoreFileMatches(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue
		}
		line = strings.TrimPrefix(line, "/")
		line = strings.TrimPrefix(line, "**/")
		line = strings.TrimSuffix(line, "/")
		line = strings.TrimSuffix(line, "/**")
		if line == ".batuta" {
			return true
		}
	}
	return false
}

// ignoreFileNames reports whether any of the named root files holds a line
// that matches .batuta. A missing file holds none.
func ignoreFileNames(root string, names ...string) (bool, error) {
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(root, name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		if ignoreFileMatches(string(data)) {
			return true, nil
		}
	}
	return false, nil
}

func checkESLint(root string, pkg *packageJSON) ([]Finding, error) {
	var findings []Finding
	for _, name := range configFiles(root, "eslint.config", jsExts) {
		findings = append(findings, sourceFinding("eslint", name, eslintFlatVerdict, root))
	}
	if len(findings) > 0 {
		return findings, nil
	}
	listed, err := ignoreFileNames(root, ".eslintignore")
	if err != nil {
		return nil, err
	}
	for _, name := range eslintLegacyNames {
		if _, err := os.Stat(filepath.Join(root, name)); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		patterns := legacyPatternsStructured
		if strings.HasSuffix(name, ".js") || strings.HasSuffix(name, ".cjs") {
			patterns = legacyPatternsSource
		}
		findings = append(findings, sourceFinding("eslint", name, legacyVerdict(listed, patterns), root))
	}
	if pkg != nil {
		if raw, ok := pkg.Raw["eslintConfig"]; ok {
			f := Finding{Tool: "eslint", Config: "package.json", Key: "eslintConfig"}
			var value any
			if err := json.Unmarshal(raw, &value); err != nil {
				f.Status, f.Hint = Unknown, err.Error()
			} else {
				f.Status, f.Hint = legacyStatus(listed || ignorePatternsBatuta(value))
			}
			findings = append(findings, f)
		}
	}
	if len(findings) == 0 && scriptTools(pkg)["eslint"] {
		findings = append(findings, scriptFinding("eslint", eslintScriptHint))
	}
	return findings, nil
}

func legacyVerdict(listed bool, patterns func(src string) (bool, error)) func(string) (string, string, error) {
	return func(src string) (string, string, error) {
		inConfig, err := patterns(src)
		if err != nil {
			return "", "", err
		}
		status, hint := legacyStatus(listed || inConfig)
		return status, hint, nil
	}
}

func legacyStatus(ignored bool) (string, string) {
	if ignored {
		return Ignored, ""
	}
	return Missing, eslintLegacyHint
}

func eslintFlatVerdict(src string) (string, string, error) {
	occurrences, err := keyLiterals(src, "ignores")
	if err != nil {
		return "", "", err
	}
	for _, o := range occurrences {
		if hasBatuta(o.Literals) && onlyName(o.Siblings) {
			return Ignored, "", nil
		}
	}
	global, err := callLiterals(src, "globalIgnores")
	if err != nil {
		return "", "", err
	}
	if hasBatuta(global) {
		return Ignored, "", nil
	}
	literals, err := stringLiterals(src)
	if err != nil {
		return "", "", err
	}
	if hasBatuta(literals) {
		return Unknown, eslintUnknownHint, nil
	}
	return Missing, eslintMissingHint, nil
}

// onlyName reports whether an object has no key besides name, which is what
// makes its ignores key global.
func onlyName(siblings []string) bool {
	for _, s := range siblings {
		if s != "name" {
			return false
		}
	}
	return true
}

func legacyPatternsSource(src string) (bool, error) {
	return keyedBatuta(src, "ignorePatterns")
}

// legacyPatternsStructured reads a JSON, JSONC or YAML legacy config.
func legacyPatternsStructured(src string) (bool, error) {
	value, err := parseJSONC([]byte(src))
	if err != nil {
		var object map[string]any
		if yamlErr := yaml.Unmarshal([]byte(src), &object); yamlErr != nil {
			return false, yamlErr
		}
		value = object
	}
	return ignorePatternsBatuta(value), nil
}

func ignorePatternsBatuta(value any) bool {
	object, _ := value.(map[string]any)
	switch patterns := object["ignorePatterns"].(type) {
	case string, []any:
		return valueHasBatuta(patterns)
	}
	return false
}

func checkPrettier(root string, pkg *packageJSON) ([]Finding, error) {
	var configs []Finding
	for _, name := range configFiles(root, ".prettierrc", prettierRCExts) {
		configs = append(configs, Finding{Tool: "prettier", Config: name})
	}
	for _, name := range configFiles(root, "prettier.config", jsExts) {
		configs = append(configs, Finding{Tool: "prettier", Config: name})
	}
	if pkg != nil {
		if _, ok := pkg.Raw["prettier"]; ok {
			configs = append(configs, Finding{Tool: "prettier", Config: "package.json", Key: "prettier"})
		}
	}
	if len(configs) == 0 {
		if !scriptTools(pkg)["prettier"] {
			return nil, nil
		}
		configs = append(configs, Finding{Tool: "prettier", Config: "package.json", Key: "scripts"})
	}
	ignored, err := ignoreFileNames(root, ".prettierignore", ".gitignore")
	if err != nil {
		return nil, err
	}
	for i := range configs {
		if ignored {
			configs[i].Status = Ignored
		} else {
			configs[i].Status, configs[i].Hint = Missing, prettierMissingHint
		}
	}
	return configs, nil
}
