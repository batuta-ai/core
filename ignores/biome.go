package ignores

import (
	"errors"
	"strings"
)

const (
	biomeIncludesHint = `add "!**/.batuta" to files.includes, after the positive patterns`
	biomeAbsentHint   = `add "files": { "includes": ["**", "!**/.batuta"] }`
	biomeV1Hint       = `add ".batuta" to files.ignore`
	biomeUnknownHint  = `version unknown: v2 adds "!**/.batuta" to files.includes after "**", v1 adds ".batuta" to files.ignore`
	biomeScriptHint   = `create biome.json: v2 "files": { "includes": ["**", "!**/.batuta"] }, v1 "files": { "ignore": [".batuta"] }`
)

var biomeConfigNames = []string{"biome.json", "biome.jsonc"}

func checkBiome(root string, pkg *packageJSON) ([]Finding, error) {
	var findings []Finding
	for _, name := range configFiles(root, "", biomeConfigNames) {
		verdict := func(src string) (string, string, error) { return biomeVerdict(src, pkg) }
		findings = append(findings, sourceFinding("biome", name, verdict, root))
	}
	if len(findings) == 0 && scriptTools(pkg)["biome"] {
		findings = append(findings, scriptFinding("biome", biomeScriptHint))
	}
	return findings, nil
}

func biomeVerdict(src string, pkg *packageJSON) (string, string, error) {
	value, err := parseJSONC([]byte(src))
	if err != nil {
		return "", "", err
	}
	config, ok := value.(map[string]any)
	if !ok {
		return "", "", errors.New("biome config must be an object")
	}
	files, _ := config["files"].(map[string]any)
	includes, hasIncludes, err := stringList(files, "includes")
	if err != nil {
		return "", "", err
	}
	ignore, hasIgnore, err := stringList(files, "ignore")
	if err != nil {
		return "", "", err
	}
	v2Ignored := hasIncludes && biomeIncludesIgnore(includes)
	v1Ignored := hasIgnore && biomeIgnoreNamesBatuta(ignore)

	switch biomeMajor(config, pkg) {
	case 2:
		switch {
		case v2Ignored:
			return Ignored, "", nil
		case hasIncludes:
			return Missing, biomeIncludesHint, nil
		}
		return Missing, biomeAbsentHint, nil
	case 1:
		if v1Ignored {
			return Ignored, "", nil
		}
		return Missing, biomeV1Hint, nil
	}
	if v2Ignored || v1Ignored {
		return Ignored, "", nil
	}
	return Unknown, biomeUnknownHint, nil
}

// stringList reads the list under key; ok is false when the key is absent.
func stringList(object map[string]any, key string) (list []string, ok bool, err error) {
	raw, present := object[key]
	if !present {
		return nil, false, nil
	}
	items, isList := raw.([]any)
	if !isList {
		return nil, false, errors.New("files." + key + " must be a list of strings")
	}
	for _, item := range items {
		s, isString := item.(string)
		if !isString {
			return nil, false, errors.New("files." + key + " must be a list of strings")
		}
		list = append(list, s)
	}
	return list, true, nil
}

// biomeIncludesIgnore evaluates Biome v2 patterns in order: the last one that
// matches .batuta decides, and a negated one skips it.
func biomeIncludesIgnore(patterns []string) bool {
	ignored := false
	for _, p := range patterns {
		negated := strings.HasPrefix(p, "!")
		p = strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(p, "!!"), "!"), "./")
		if biomePatternMatches(p) {
			ignored = negated
		}
	}
	return ignored
}

func biomePatternMatches(p string) bool {
	first, _, _ := strings.Cut(p, "/")
	return p == "**" || p == "**/*" || first == ".batuta" || p == "**/.batuta" || strings.HasPrefix(p, "**/.batuta/")
}

// biomeIgnoreNamesBatuta reports whether a Biome v1 files.ignore entry ignores
// the root .batuta: only the bare name or its "**/" form, with an optional
// trailing slash or recursive suffix.
func biomeIgnoreNamesBatuta(entries []string) bool {
	for _, e := range entries {
		e = strings.TrimPrefix(e, "./")
		switch {
		case strings.HasSuffix(e, "/**/*"):
			e = strings.TrimSuffix(e, "/**/*")
		case strings.HasSuffix(e, "/**"):
			e = strings.TrimSuffix(e, "/**")
		default:
			e = strings.TrimSuffix(e, "/")
		}
		if e == ".batuta" || e == "**/.batuta" {
			return true
		}
	}
	return false
}

// biomeMajor returns the Biome major version from the $schema URL, else from
// the @biomejs/biome entry of package.json; 0 means unknown.
func biomeMajor(config map[string]any, pkg *packageJSON) int {
	if schema, _ := config["$schema"].(string); schema != "" {
		if _, rest, found := strings.Cut(schema, "/schemas/"); found {
			version, _, _ := strings.Cut(rest, "/")
			if major := majorVersion(version); major > 0 {
				return major
			}
		}
	}
	if pkg == nil {
		return 0
	}
	version, ok := pkg.Dependencies["@biomejs/biome"]
	if !ok {
		version = pkg.DevDependencies["@biomejs/biome"]
	}
	return majorVersion(version)
}

// majorVersion reads the leading number of a version or range, after the
// operators npm puts in front of it; anything else yields 0.
func majorVersion(version string) int {
	version = strings.TrimLeft(version, "^~><=v ")
	major := 0
	digits := 0
	for _, c := range version {
		if c < '0' || c > '9' {
			break
		}
		major = major*10 + int(c-'0')
		digits++
	}
	if digits == 0 || digits > 4 {
		return 0
	}
	return major
}
