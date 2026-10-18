package ignores

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	tsFilesHint   = "remove .batuta from files: exclude does not apply to files"
	tsIncludeHint = `remove .batuta from include, or add ".batuta" to exclude`

	tsMaxConfigs = 5
)

// tsConfig is one tsconfig file read for the root config's extends chain.
type tsConfig struct {
	dir     string
	fields  map[string]any
	extends []*tsConfig
}

// tsLoader reads the relative extends chain and enforces its limits.
type tsLoader struct {
	root  string
	count int
	stack map[string]bool
}

func checkTypeScript(root string, _ *packageJSON) ([]Finding, error) {
	const name = "tsconfig.json"
	if _, err := os.Stat(filepath.Join(root, name)); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	f := Finding{Tool: "typescript", Config: name}
	f.Status, f.Hint = tsVerdict(root, name)
	return []Finding{f}, nil
}

func tsVerdict(root, name string) (string, string) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return Unknown, err.Error()
	}
	loader := &tsLoader{root: realRoot, stack: map[string]bool{}}
	cfg, err := loader.load(filepath.Join(realRoot, name), filepath.Join(realRoot, name))
	if err != nil {
		return Unknown, err.Error()
	}
	files, err := cfg.paths("files")
	if err != nil {
		return Unknown, err.Error()
	}
	include, err := cfg.paths("include")
	if err != nil {
		return Unknown, err.Error()
	}
	exclude, err := cfg.paths("exclude")
	if err != nil {
		return Unknown, err.Error()
	}
	switch {
	case anyPath(files, tsSelects):
		return Missing, tsFilesHint
	case !anyPath(include, tsSelects):
		return NotNeeded, ""
	case anyPath(exclude, tsCovers):
		return Ignored, ""
	}
	return Missing, tsIncludeHint
}

// load reads the config at file and, recursively, the relative configs it
// extends. real is file with symlinks resolved; entries resolve against the
// directory of file, as tsc does, and loops are detected on real.
func (l *tsLoader) load(file, real string) (*tsConfig, error) {
	if l.stack[real] {
		return nil, fmt.Errorf("extends loops back to %s", l.relative(file))
	}
	l.count++
	if l.count > tsMaxConfigs {
		return nil, fmt.Errorf("extends chain is longer than %d files", tsMaxConfigs)
	}
	l.stack[real] = true
	defer delete(l.stack, real)

	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	value, err := parseJSONC(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", l.relative(file), err)
	}
	fields, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an object", l.relative(file))
	}
	cfg := &tsConfig{dir: path.Dir(l.relative(file)), fields: fields}
	entries, err := extendsEntries(fields["extends"])
	if err != nil {
		return nil, fmt.Errorf("%s: %w", l.relative(file), err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry, "./") && !strings.HasPrefix(entry, "../") {
			continue
		}
		target, resolved, err := l.resolve(filepath.Dir(file), entry)
		if err != nil {
			return nil, err
		}
		base, err := l.load(target, resolved)
		if err != nil {
			return nil, err
		}
		cfg.extends = append(cfg.extends, base)
	}
	return cfg, nil
}

// resolve finds the file a relative extends entry names, with or without the
// .json suffix, and requires it to stay inside the root once symlinks resolve.
// It returns the file as named and with symlinks resolved.
func (l *tsLoader) resolve(dir, entry string) (string, string, error) {
	target := filepath.Join(dir, filepath.FromSlash(entry))
	if info, err := os.Stat(target); err != nil || info.IsDir() {
		target += ".json"
	}
	real, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", "", fmt.Errorf("extends %q: %w", entry, err)
	}
	rel, err := filepath.Rel(l.root, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("extends %q resolves outside the workspace root", entry)
	}
	return target, real, nil
}

func (l *tsLoader) relative(file string) string {
	rel, err := filepath.Rel(l.root, file)
	if err != nil {
		return file
	}
	return filepath.ToSlash(rel)
}

func extendsEntries(value any) ([]string, error) {
	switch v := value.(type) {
	case nil:
		return nil, nil
	case string:
		return []string{v}, nil
	case []any:
		entries := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, errors.New("extends must be a string or a list of strings")
			}
			entries = append(entries, s)
		}
		return entries, nil
	}
	return nil, errors.New("extends must be a string or a list of strings")
}

// paths returns the entries of key from the nearest config that sets it,
// each resolved against that config's directory. A later extends entry
// overrides an earlier one, as in tsc.
func (c *tsConfig) paths(key string) ([]string, error) {
	if raw, ok := c.fields[key]; ok {
		items, isList := raw.([]any)
		if !isList {
			return nil, fmt.Errorf("%s must be a list of strings", key)
		}
		resolved := make([]string, 0, len(items))
		for _, item := range items {
			s, isString := item.(string)
			if !isString {
				return nil, fmt.Errorf("%s must be a list of strings", key)
			}
			resolved = append(resolved, path.Join(c.dir, s))
		}
		return resolved, nil
	}
	for i := len(c.extends) - 1; i >= 0; i-- {
		got, err := c.extends[i].paths(key)
		if err != nil || got != nil {
			return got, err
		}
	}
	return nil, nil
}

func anyPath(entries []string, match func(string) bool) bool {
	for _, e := range entries {
		if match(e) {
			return true
		}
	}
	return false
}

// tsSelects reports whether an include or files entry names .batuta.
func tsSelects(p string) bool {
	first, _, _ := strings.Cut(p, "/")
	return first == ".batuta" || p == "**/.batuta" || strings.HasPrefix(p, "**/.batuta/")
}

// tsCovers reports whether an exclude entry covers .batuta.
func tsCovers(p string) bool {
	p = strings.TrimSuffix(p, "/**/*")
	p = strings.TrimSuffix(p, "/**")
	return p == ".batuta" || p == "**/.batuta"
}
