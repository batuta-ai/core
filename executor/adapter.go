// Package executor turns an adapter file (`skills/batuta/adapters/<name>.md`)
// into argv the loop can run. The adapter frontmatter is the machine
// contract the skills already lint: one `key: scalar` per line, quoted or
// plain, with placeholders such as {brief}, {model_flags} or {cwd}. The
// `run` line is written as a shell command for humans; here it is tokenized
// once, placeholders are substituted per token, and the result is executed
// directly — never through a shell.
package executor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Adapter is the parsed frontmatter of one adapter file.
type Adapter struct {
	Name            string
	Executable      string
	Run             string
	RunFile         string
	ModelFlags      string
	Readonly        string
	Available       string
	Models          string
	Finished        string
	LimitRegex      string
	CwdFlag         string
	BriefLimitLines int
	Fields          map[string]string
}

var (
	ErrAdapterInvalid = errors.New("executor: adapter frontmatter is invalid")

	frontmatterLine = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*):(?:\s+(.*))?$`)
	adapterName     = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
)

// LoadAdapter reads `<skillsRoot>/adapters/<name>.md`.
func LoadAdapter(skillsRoot, name string) (Adapter, error) {
	if !adapterName.MatchString(name) {
		return Adapter{}, fmt.Errorf("%w: adapter name %q", ErrAdapterInvalid, name)
	}
	path := filepath.Join(skillsRoot, "adapters", name+".md")
	payload, err := os.ReadFile(path)
	if err != nil {
		return Adapter{}, fmt.Errorf("executor: adapter %s is not installed (%s)", name, path)
	}
	adapter, err := ParseAdapter(payload)
	if err != nil {
		return Adapter{}, fmt.Errorf("%s: %w", path, err)
	}
	if adapter.Name != name {
		return Adapter{}, fmt.Errorf("%w: %s declares name %q", ErrAdapterInvalid, path, adapter.Name)
	}
	return adapter, nil
}

// ParseAdapter parses the frontmatter block of an adapter file with the
// same rules as the skills lint: plain scalars end at ` #`, quoted scalars
// keep everything between the quotes.
func ParseAdapter(payload []byte) (Adapter, error) {
	text := string(payload)
	if !strings.HasPrefix(text, "---\n") {
		return Adapter{}, fmt.Errorf("%w: no frontmatter block", ErrAdapterInvalid)
	}
	body := text[4:]
	end := strings.Index(body, "\n---\n")
	if end < 0 {
		if strings.HasSuffix(body, "\n---") {
			end = len(body) - 4
		} else {
			return Adapter{}, fmt.Errorf("%w: unterminated frontmatter block", ErrAdapterInvalid)
		}
	}
	fields := map[string]string{}
	for index, line := range strings.Split(body[:end], "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		match := frontmatterLine.FindStringSubmatch(line)
		if match == nil {
			return Adapter{}, fmt.Errorf("%w: line %d is not `key: value`", ErrAdapterInvalid, index+2)
		}
		key, value := match[1], strings.TrimSpace(match[2])
		switch {
		case strings.HasPrefix(value, "'"), strings.HasPrefix(value, `"`):
			if len(value) < 2 || value[len(value)-1] != value[0] {
				return Adapter{}, fmt.Errorf("%w: line %d has an unterminated quoted scalar", ErrAdapterInvalid, index+2)
			}
			value = value[1 : len(value)-1]
		default:
			if at := strings.Index(value, " #"); at >= 0 {
				value = strings.TrimSpace(value[:at])
			}
		}
		fields[key] = value
	}
	for _, required := range []string{"name", "run", "readonly", "available", "models", "finished"} {
		if _, present := fields[required]; !present {
			return Adapter{}, fmt.Errorf("%w: missing %s", ErrAdapterInvalid, required)
		}
	}
	adapter := Adapter{
		Name: fields["name"], Executable: fields["executable"], Run: fields["run"], RunFile: fields["run_file"],
		ModelFlags: fields["model_flags"], Readonly: fields["readonly"], Available: fields["available"],
		Models: fields["models"], Finished: fields["finished"], LimitRegex: fields["limit_regex"],
		CwdFlag: fields["cwd_flag"], BriefLimitLines: 100, Fields: fields,
	}
	if raw, present := fields["brief_limit_lines"]; present {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 {
			return Adapter{}, fmt.Errorf("%w: brief_limit_lines must be a positive integer", ErrAdapterInvalid)
		}
		adapter.BriefLimitLines = limit
	}
	if !adapterName.MatchString(adapter.Name) {
		return Adapter{}, fmt.Errorf("%w: name %q", ErrAdapterInvalid, adapter.Name)
	}
	if adapter.LimitRegex != "" {
		if _, err := regexp.Compile(adapter.LimitRegex); err != nil {
			return Adapter{}, fmt.Errorf("%w: limit_regex does not compile: %v", ErrAdapterInvalid, err)
		}
	}
	return adapter, nil
}

// IsSelf says whether the adapter is the conducting host itself, which the
// loop cannot invoke.
func (a Adapter) IsSelf() bool {
	return a.Name == "self" || a.Run == "self"
}

// Tokenize splits a run line the way a POSIX shell would split words —
// single quotes literal, double quotes with backslash escapes, unquoted
// backslash escapes — and drops the `< /dev/null` redirection the adapters
// write for humans: the runner closes stdin itself. Any other shell syntax
// (pipes, `&&`, other redirections, substitutions) is rejected: a run line
// is argv, not a script.
func Tokenize(line string) ([]string, error) {
	var (
		tokens  []string
		current strings.Builder
		inWord  bool
		quote   rune
	)
	flush := func() {
		if inWord {
			tokens = append(tokens, current.String())
			current.Reset()
			inWord = false
		}
	}
	runes := []rune(line)
	for index := 0; index < len(runes); index++ {
		char := runes[index]
		switch {
		case quote == '\'':
			if char == '\'' {
				quote = 0
			} else {
				current.WriteRune(char)
			}
		case quote == '"':
			switch char {
			case '"':
				quote = 0
			case '\\':
				if index+1 < len(runes) && strings.ContainsRune(`"\$`+"`", runes[index+1]) {
					index++
					current.WriteRune(runes[index])
				} else {
					current.WriteRune(char)
				}
			case '$', '`':
				return nil, fmt.Errorf("executor: run line uses shell expansion (%q) — adapters carry argv only", string(char))
			default:
				current.WriteRune(char)
			}
		case char == '\'' || char == '"':
			quote = char
			inWord = true
		case char == '\\':
			if index+1 < len(runes) {
				index++
				current.WriteRune(runes[index])
				inWord = true
			}
		case char == ' ' || char == '\t':
			flush()
		case char == '|' || char == '&' || char == ';' || char == '>' || char == '$' || char == '`' || char == '(' || char == ')':
			return nil, fmt.Errorf("executor: run line uses shell syntax (%q) — adapters carry argv only", string(char))
		case char == '<':
			flush()
			rest := strings.TrimSpace(string(runes[index+1:]))
			if !strings.HasPrefix(rest, "/dev/null") {
				return nil, errors.New("executor: only `< /dev/null` is accepted on a run line")
			}
			index += len(runes) - index - 1
			trimmed := strings.TrimSpace(rest[len("/dev/null"):])
			if trimmed != "" {
				more, err := Tokenize(trimmed)
				if err != nil {
					return nil, err
				}
				tokens = append(tokens, more...)
			}
		default:
			current.WriteRune(char)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, errors.New("executor: run line has an unterminated quote")
	}
	flush()
	if len(tokens) == 0 {
		return nil, errors.New("executor: run line is empty")
	}
	return tokens, nil
}

// Request is what one invocation substitutes into the adapter's line.
type Request struct {
	Brief     string
	BriefFile string // absolute path of the brief on disk; required when the brief exceeds brief_limit_lines
	Prompt    string // read-only invocations
	Cwd       string
	Model     string // routing.DefaultModel omits the model flags
	Effort    string
}

// Invocation is a resolved command: an executable name (resolved to an
// absolute path by the runner), its arguments and the working directory.
type Invocation struct {
	Executable string
	Args       []string
	Dir        string
	UsedFile   bool
}

// DefaultModel mirrors routing.DefaultModel without importing routing.
const DefaultModel = "default"

// Command builds the executor invocation for a brief, choosing `run_file`
// when the brief is longer than the adapter's line budget.
func (a Adapter) Command(request Request) (Invocation, error) {
	if a.IsSelf() {
		return Invocation{}, errors.New("executor: the self adapter is the conducting session; the loop cannot invoke it")
	}
	if strings.TrimSpace(request.Brief) == "" || !filepath.IsAbs(request.Cwd) {
		return Invocation{}, errors.New("executor: a brief and an absolute working directory are required")
	}
	line := a.Run
	useFile := false
	if lines := strings.Count(request.Brief, "\n") + 1; lines > a.BriefLimitLines && a.RunFile != "" {
		if !filepath.IsAbs(request.BriefFile) {
			return Invocation{}, errors.New("executor: the brief exceeds brief_limit_lines and no brief file was written")
		}
		line = a.RunFile
		useFile = true
	}
	invocation, err := a.build(line, request)
	if err != nil {
		return Invocation{}, err
	}
	invocation.UsedFile = useFile
	return invocation, nil
}

// ReadonlyCommand builds the adapter's read-only invocation for a prompt.
func (a Adapter) ReadonlyCommand(request Request) (Invocation, error) {
	if a.IsSelf() {
		return Invocation{}, errors.New("executor: the self adapter has no read-only line the loop can run")
	}
	if strings.TrimSpace(request.Prompt) == "" || !filepath.IsAbs(request.Cwd) {
		return Invocation{}, errors.New("executor: a prompt and an absolute working directory are required")
	}
	return a.build(a.Readonly, request)
}

func (a Adapter) build(line string, request Request) (Invocation, error) {
	tokens, err := Tokenize(line)
	if err != nil {
		return Invocation{}, err
	}
	modelFlags, err := a.expandFlags(a.ModelFlags, request)
	if err != nil {
		return Invocation{}, err
	}
	if request.Model == DefaultModel || request.Model == "" {
		modelFlags = nil
	}
	cwdFlags := []string{}
	if a.CwdFlag != "" && a.CwdFlag != "env" {
		cwdFlags, err = a.expandFlags(a.CwdFlag, request)
		if err != nil {
			return Invocation{}, err
		}
	}
	args := make([]string, 0, len(tokens)+len(modelFlags)+len(cwdFlags))
	for _, token := range tokens {
		switch token {
		case "{model_flags}":
			args = append(args, modelFlags...)
		case "{cwd_flag}":
			args = append(args, cwdFlags...)
		default:
			args = append(args, substitute(token, request))
		}
	}
	if len(args) == 0 {
		return Invocation{}, errors.New("executor: run line resolved to nothing")
	}
	return Invocation{Executable: args[0], Args: args[1:], Dir: request.Cwd}, nil
}

func (a Adapter) expandFlags(template string, request Request) ([]string, error) {
	if strings.TrimSpace(template) == "" {
		return nil, nil
	}
	tokens, err := Tokenize(template)
	if err != nil {
		return nil, err
	}
	expanded := make([]string, 0, len(tokens))
	for _, token := range tokens {
		expanded = append(expanded, substitute(token, request))
	}
	return expanded, nil
}

func substitute(token string, request Request) string {
	replacer := strings.NewReplacer(
		"{brief}", request.Brief,
		"{brief_file}", request.BriefFile,
		"{prompt}", request.Prompt,
		"{cwd}", request.Cwd,
		"{model}", request.Model,
		"{effort}", request.Effort,
	)
	return replacer.Replace(token)
}
