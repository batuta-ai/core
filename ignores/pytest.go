package ignores

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const pytestMissingHint = "keep '.*' in norecursedirs, or add .batuta"

// pytestSettings holds the two options that decide whether pytest enters .batuta.
type pytestSettings struct {
	norecursedirs    []string
	hasNorecursedirs bool
	testpaths        []string
}

// pytestSource is one place pytest may read its options from, in precedence
// order. read returns nil when the file does not hold the pytest options.
type pytestSource struct {
	name string
	read func(data []byte) (*pytestSettings, error)
}

var pytestSources = []pytestSource{
	{"pytest.toml", readPytestTOML},
	{".pytest.toml", readPytestTOML},
	{"pytest.ini", iniSection("pytest", true)},
	{".pytest.ini", iniSection("pytest", true)},
	{"pyproject.toml", readPyproject},
	{"tox.ini", iniSection("pytest", false)},
	{"setup.cfg", iniSection("tool:pytest", false)},
}

func checkPytest(root string, _ *packageJSON) ([]Finding, error) {
	for _, source := range pytestSources {
		data, err := os.ReadFile(filepath.Join(root, source.name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		f := Finding{Tool: "pytest", Config: source.name}
		if err != nil {
			f.Status, f.Hint = Unknown, err.Error()
			return []Finding{f}, nil
		}
		settings, err := source.read(data)
		if err != nil {
			f.Status, f.Hint = Unknown, err.Error()
			return []Finding{f}, nil
		}
		if settings == nil {
			continue
		}
		f.Status, f.Hint = pytestVerdict(root, settings)
		return []Finding{f}, nil
	}
	return nil, nil
}

func pytestVerdict(root string, s *pytestSettings) (string, string) {
	if !s.hasNorecursedirs {
		return NotNeeded, ""
	}
	for _, entry := range s.norecursedirs {
		if ok, _ := path.Match(entry, ".batuta"); ok {
			return Ignored, ""
		}
	}
	if testpathsAvoidBatuta(root, s.testpaths) {
		return NotNeeded, ""
	}
	return Missing, pytestMissingHint
}

// testpathsAvoidBatuta holds when every entry is a named directory outside
// .batuta and one of them exists: pytest falls back to the root when no entry
// matches.
func testpathsAvoidBatuta(root string, dirs []string) bool {
	exists := false
	for _, d := range dirs {
		first, ok := namedDirectory(d)
		if !ok || first == ".batuta" {
			return false
		}
		if info, err := os.Stat(filepath.Join(root, strings.TrimPrefix(d, "./"))); err == nil && info.IsDir() {
			exists = true
		}
	}
	return exists
}

// readPytestTOML reads the [pytest] table; a file without it is still the
// config pytest uses, with no options set.
func readPytestTOML(data []byte) (*pytestSettings, error) {
	var doc map[string]any
	if err := toml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	table, _ := doc["pytest"].(map[string]any)
	return settingsFromTable(table)
}

// readPyproject reads [tool.pytest] or [tool.pytest.ini_options]; without
// either, pyproject.toml is not a pytest config.
func readPyproject(data []byte) (*pytestSettings, error) {
	var doc map[string]any
	if err := toml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	tool, _ := doc["tool"].(map[string]any)
	table, ok := tool["pytest"].(map[string]any)
	if !ok {
		return nil, nil
	}
	if options, ok := table["ini_options"].(map[string]any); ok {
		table = options
	}
	return settingsFromTable(table)
}

func settingsFromTable(table map[string]any) (*pytestSettings, error) {
	s := &pytestSettings{}
	if raw, ok := table["norecursedirs"]; ok {
		list, err := tomlEntries("norecursedirs", raw)
		if err != nil {
			return nil, err
		}
		s.norecursedirs, s.hasNorecursedirs = list, true
	}
	if raw, ok := table["testpaths"]; ok {
		list, err := tomlEntries("testpaths", raw)
		if err != nil {
			return nil, err
		}
		s.testpaths = list
	}
	return s, nil
}

// tomlEntries takes a list of strings or a whitespace-separated string.
func tomlEntries(key string, raw any) ([]string, error) {
	switch v := raw.(type) {
	case string:
		return strings.Fields(v), nil
	case []any:
		list := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("%s must be a list of strings", key)
			}
			list = append(list, s)
		}
		return list, nil
	}
	return nil, fmt.Errorf("%s must be a list of strings or a string", key)
}

// iniSection reads the named section of an INI file. When always is set the
// file is the pytest config even without the section.
func iniSection(section string, always bool) func(data []byte) (*pytestSettings, error) {
	return func(data []byte) (*pytestSettings, error) {
		values, found := parseINISection(string(data), section)
		if !found && !always {
			return nil, nil
		}
		s := &pytestSettings{}
		if raw, ok := values["norecursedirs"]; ok {
			s.norecursedirs, s.hasNorecursedirs = iniEntries(raw), true
		}
		s.testpaths = iniEntries(values["testpaths"])
		return s, nil
	}
}

// parseINISection returns the lowercased keys of one section, a continuation
// line (indented) extending the previous value.
func parseINISection(src, section string) (values map[string]string, found bool) {
	values = map[string]string{}
	inSection := false
	key := ""
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") && line[0] != ' ' && line[0] != '\t' {
			inSection = trimmed[1:len(trimmed)-1] == section
			found = found || inSection
			key = ""
			continue
		}
		if !inSection {
			continue
		}
		if (line[0] == ' ' || line[0] == '\t') && key != "" {
			values[key] += " " + trimmed
			continue
		}
		i := strings.IndexAny(trimmed, "=:")
		if i < 0 {
			key = ""
			continue
		}
		key = strings.ToLower(strings.TrimSpace(trimmed[:i]))
		values[key] = strings.TrimSpace(trimmed[i+1:])
	}
	return values, found
}

// iniEntries splits an INI value on whitespace and unquotes each entry.
func iniEntries(value string) []string {
	fields := strings.Fields(value)
	for i, f := range fields {
		if len(f) >= 2 && (f[0] == '"' || f[0] == '\'') && f[len(f)-1] == f[0] {
			fields[i] = f[1 : len(f)-1]
		}
	}
	return fields
}
