// Package gates is the mechanical half of `references/verification.md`:
// the four gates a delivery passes before it becomes a candidate, run by
// the conductor outside the executor's session.
//
//	0 finished  the executor ended on its own terms (adapter rule)
//	1 tree      the session wrote something (a signal, not a verdict)
//	2 tests     the profile's test command passes, run here, stdin closed
//	3 verify    scope check, each criterion's proof re-run, and — on
//	            high/critical, a silent tree or a retry — an independent
//	            read-only verifier that answers TASK n: DONE|INCOMPLETE
//
// Every verdict carries bounded evidence so the run trail and the retry
// feedback can quote it.
package gates

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/batuta-ai/core/publication"
	"github.com/batuta-ai/core/worktree"
)

const (
	detailLimit = 4000
	tailLines   = 40
)

// Verdict is one gate's answer.
type Verdict struct {
	Name   string `json:"name"`
	Pass   bool   `json:"pass"`
	Signal string `json:"signal,omitempty"` // one line: what was observed
	Detail string `json:"detail,omitempty"` // bounded evidence (command output tail, paths)
}

// Report gathers the gates of one attempt. It is the verification payload
// behind a candidate's digest.
type Report struct {
	TaskID    string    `json:"task_id"`
	Execution int       `json:"execution"`
	Finished  Verdict   `json:"finished"`
	Tree      Verdict   `json:"tree"`
	Tests     Verdict   `json:"tests"`
	Scope     Verdict   `json:"scope"`
	Proofs    []Verdict `json:"proofs"`
	Verifier  *Verdict  `json:"verifier,omitempty"`
	Passed    bool      `json:"passed"`
}

// Failures lists what failed, one line each, for the retry feedback.
func (r Report) Failures() []string {
	var lines []string
	add := func(v Verdict) {
		if v.Name == "" || v.Pass {
			return
		}
		line := "gate " + v.Name + ": " + v.Signal
		if v.Detail != "" {
			line += "\n" + indent(v.Detail)
		}
		lines = append(lines, line)
	}
	add(r.Finished)
	add(r.Tree)
	add(r.Tests)
	add(r.Scope)
	for _, proof := range r.Proofs {
		add(proof)
	}
	if r.Verifier != nil {
		add(*r.Verifier)
	}
	return lines
}

// Summary is the one-line gate row of a run trail.
func (r Report) Summary() string {
	mark := func(v Verdict, silentWord string) string {
		if v.Name == "" {
			return "n/a"
		}
		if v.Pass {
			if silentWord != "" && strings.Contains(v.Signal, "silent") {
				return silentWord
			}
			return "pass"
		}
		return "fail"
	}
	proofs := "n/a"
	if len(r.Proofs) > 0 {
		passed := 0
		for _, proof := range r.Proofs {
			if proof.Pass {
				passed++
			}
		}
		proofs = fmt.Sprintf("%d/%d", passed, len(r.Proofs))
	}
	verifier := "n/a"
	if r.Verifier != nil {
		verifier = mark(*r.Verifier, "")
	}
	return fmt.Sprintf("0 %s · 1 %s · 2 %s · 3 scope %s, proofs %s, verifier %s",
		mark(r.Finished, ""), mark(r.Tree, "silent"), mark(r.Tests, ""), mark(r.Scope, ""), proofs, verifier)
}

// Canonical returns the report as the canonical JSON object the
// integration layer expects (sorted keys, compact) and its sha256 digest.
func (r Report) Canonical() ([]byte, string, error) {
	encoded, err := json.Marshal(r)
	if err != nil {
		return nil, "", err
	}
	var object map[string]any
	if err := json.Unmarshal(encoded, &object); err != nil {
		return nil, "", err
	}
	canonical, err := json.Marshal(object)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(canonical)
	return canonical, "sha256:" + hex.EncodeToString(sum[:]), nil
}

// Finished is gate 0 from the executor's result.
func Finished(finished, timedOut, rateLimited bool, exitCode int, outputTail string) Verdict {
	verdict := Verdict{Name: "finished", Pass: finished, Detail: bound(outputTail)}
	switch {
	case rateLimited:
		verdict.Pass = false
		verdict.Signal = "the executor hit a usage or rate limit"
	case timedOut:
		verdict.Signal = "the executor exceeded its time budget"
	case finished:
		verdict.Signal = "exit 0"
	default:
		verdict.Signal = "exit " + strconv.Itoa(exitCode)
	}
	return verdict
}

// Tree is gate 1: did the worktree change between two signatures.
func Tree(before, after publication.WorktreeState) Verdict {
	changed := before != after
	verdict := Verdict{Name: "tree", Pass: true, Signal: "the session wrote to the tree"}
	if !changed {
		verdict.Signal = "silent: the session wrote nothing"
	}
	return verdict
}

// ShellRunner runs a user-authored command line (the profile's test command,
// a criterion's proof) through `sh -c` with stdin closed. These lines are
// the user's own, so a shell is the contract; executors never go this way.
type ShellRunner struct {
	Runner  publication.CommandRunner
	Shell   string
	Timeout time.Duration
}

// NewShellRunner resolves /bin/sh (or sh on PATH).
func NewShellRunner(timeout time.Duration) (ShellRunner, error) {
	shell := "/bin/sh"
	resolver := publication.ExecutableResolver{}
	if _, err := resolver.Resolve(shell); err != nil {
		resolved, err := resolver.Resolve("sh")
		if err != nil {
			return ShellRunner{}, errors.New("gates: no POSIX shell found")
		}
		shell = resolved
	}
	return ShellRunner{Runner: publication.ExecRunner{}, Shell: shell, Timeout: timeout}, nil
}

// Run executes one command line in dir and returns exit code and output.
func (s ShellRunner) Run(ctx context.Context, dir, command string) (int, string, error) {
	if strings.TrimSpace(command) == "" {
		return -1, "", errors.New("gates: empty command")
	}
	runCtx := ctx
	cancel := context.CancelFunc(func() {})
	if s.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, s.Timeout)
	}
	defer cancel()
	result, err := s.Runner.Run(runCtx, publication.Command{
		Executable: s.Shell, Args: []string{"-c", command}, Directory: dir,
		Environment: []string{"CI=1", "BATUTA=1"}, StdoutLimit: 4 << 20, StderrLimit: 1 << 20,
	})
	output := string(result.Stdout)
	if len(result.Stderr) > 0 {
		output += "\n" + string(result.Stderr)
	}
	if err != nil && result.ExitCode < 0 {
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			return -1, output + "\n(timed out after " + s.Timeout.String() + ")", nil
		}
		return -1, output, err
	}
	return result.ExitCode, output, nil
}

// Tests is gate 2: the profile's test command, run by the conductor.
func Tests(ctx context.Context, shell ShellRunner, dir, command string) Verdict {
	code, output, err := shell.Run(ctx, dir, command)
	verdict := Verdict{Name: "tests", Pass: code == 0 && err == nil, Detail: bound(tail(output))}
	switch {
	case err != nil:
		verdict.Signal = "could not run `" + command + "`: " + err.Error()
	case code == 0:
		verdict.Signal = "`" + command + "` passed"
	default:
		verdict.Signal = fmt.Sprintf("`%s` exited %d", command, code)
	}
	return verdict
}

// ValidScope checks that a Scope list is contained in the repository: no
// absolute paths, no `..` segments, no empty entries.
func ValidScope(entries []string) error {
	for _, entry := range entries {
		clean := path.Clean(filepath.ToSlash(strings.TrimSpace(entry)))
		if clean == "" || clean == "." || strings.HasPrefix(clean, "/") || clean == ".." || strings.HasPrefix(clean, "../") ||
			strings.Contains(clean, "/../") || strings.ContainsAny(clean, "\x00\n") {
			return fmt.Errorf("gates: scope entry %q is not contained in the repository", entry)
		}
	}
	return nil
}

// Scope is the scope check of gate 3: every changed path must match an
// entry of the brief's Scope — an exact path, a directory prefix, or a
// glob (path.Match on the whole path, `**` matching across separators).
// Managed state is reported, never failed on. An empty Scope means the
// plan declared none: the check passes with a signal.
func Scope(changed, scope []string) Verdict {
	verdict := Verdict{Name: "scope", Pass: true}
	if len(scope) == 0 {
		verdict.Signal = "no Scope declared; every change accepted"
		return verdict
	}
	var outside, managed []string
	for _, changedPath := range changed {
		clean := filepath.ToSlash(changedPath)
		if worktree.IsManaged(clean) {
			managed = append(managed, clean)
			continue
		}
		if !InScope(clean, scope) {
			outside = append(outside, clean)
		}
	}
	switch {
	case len(outside) > 0:
		verdict.Pass = false
		verdict.Signal = fmt.Sprintf("%d path(s) outside Scope", len(outside))
		verdict.Detail = strings.Join(outside, "\n")
	case len(managed) > 0:
		verdict.Signal = "within Scope; managed state also touched: " + strings.Join(managed, ", ")
	default:
		verdict.Signal = "within Scope"
	}
	return verdict
}

// InScope says whether one path matches any Scope entry.
func InScope(changed string, scope []string) bool {
	for _, entry := range scope {
		pattern := path.Clean(filepath.ToSlash(strings.TrimSpace(entry)))
		if pattern == changed || strings.HasPrefix(changed, pattern+"/") {
			return true
		}
		if globMatch(pattern, changed) {
			return true
		}
	}
	return false
}

func globMatch(pattern, target string) bool {
	if !strings.ContainsAny(pattern, "*?[") {
		return false
	}
	if strings.Contains(pattern, "**") {
		expression := regexp.QuoteMeta(pattern)
		expression = strings.ReplaceAll(expression, `\*\*/`, `(?:.*/)?`)
		expression = strings.ReplaceAll(expression, `\*\*`, `.*`)
		expression = strings.ReplaceAll(expression, `\*`, `[^/]*`)
		expression = strings.ReplaceAll(expression, `\?`, `[^/]`)
		matched, err := regexp.MatchString("^"+expression+"$", target)
		return err == nil && matched
	}
	matched, err := path.Match(pattern, target)
	return err == nil && matched
}

// Criterion is one acceptance criterion: what must hold and the command
// that proves it. A criterion written without ` → <command>` has no
// mechanical proof; the verifier judges it.
type Criterion struct {
	Text  string `json:"text"`
	Proof string `json:"proof,omitempty"`
}

// ParseCriteria splits `criterion → proof` entries. The arrow may be `→`
// or `->`; the last arrow on the line separates text from proof.
func ParseCriteria(accept []string) []Criterion {
	criteria := make([]Criterion, 0, len(accept))
	for _, entry := range accept {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		criterion := Criterion{Text: entry}
		for _, arrow := range []string{" → ", " -> ", "→", "->"} {
			if index := strings.LastIndex(entry, arrow); index > 0 {
				text, proof := strings.TrimSpace(entry[:index]), strings.TrimSpace(entry[index+len(arrow):])
				if text != "" && proof != "" {
					criterion = Criterion{Text: text, Proof: strings.Trim(proof, "`")}
				}
				break
			}
		}
		criteria = append(criteria, criterion)
	}
	return criteria
}

// Proofs re-runs each criterion's proof in dir. Criteria without a proof
// pass here with a signal and are left to the verifier.
func Proofs(ctx context.Context, shell ShellRunner, dir string, criteria []Criterion) []Verdict {
	verdicts := make([]Verdict, 0, len(criteria))
	for index, criterion := range criteria {
		name := "proof " + strconv.Itoa(index+1)
		if criterion.Proof == "" {
			verdicts = append(verdicts, Verdict{Name: name, Pass: true, Signal: criterion.Text + " — no proof command; left to the verifier"})
			continue
		}
		code, output, err := shell.Run(ctx, dir, criterion.Proof)
		verdict := Verdict{Name: name, Pass: code == 0 && err == nil, Detail: bound(tail(output))}
		switch {
		case err != nil:
			verdict.Signal = criterion.Text + " — could not run `" + criterion.Proof + "`: " + err.Error()
		case code == 0:
			verdict.Signal = criterion.Text + " — `" + criterion.Proof + "` passed"
		default:
			verdict.Signal = fmt.Sprintf("%s — `%s` exited %d", criterion.Text, criterion.Proof, code)
		}
		verdicts = append(verdicts, verdict)
	}
	return verdicts
}

// VerifierPrompt is the read-only verifier's brief: one line per criterion.
func VerifierPrompt(taskTitle string, criteria []Criterion, proofs []Verdict, base string) string {
	var b strings.Builder
	b.WriteString("You are an independent read-only verifier. Do not create, edit or delete any file. ")
	b.WriteString("Do not run commands that change state: no git writes, no package managers, no mv, rm, > or >>. ")
	b.WriteString("Reading, ls, grep, find, cat, head and running the project's tests are allowed.\n\n")
	b.WriteString("Task: " + taskTitle + "\n")
	if base != "" {
		b.WriteString("The work under review is `git diff " + base + "...HEAD` in the current directory.\n")
	}
	b.WriteString("\nFor each criterion below, check it against the current tree and print exactly one line, in order, and nothing else after them:\n")
	b.WriteString("  TASK <n>: DONE\n  TASK <n>: INCOMPLETE — <what is missing>\n\n")
	b.WriteString("Rules: one TASK line per criterion, no grouping, no other text after them. Missing code, a TODO, a placeholder or a missing test is INCOMPLETE. In doubt, INCOMPLETE.\n")
	b.WriteString("A criterion whose proof passed is INCOMPLETE only for something you can point at in the diff: missing code, a placeholder, a test that does not test the criterion. Never because a command could not run in your environment; the conductor already ran the proof.\n")
	b.WriteString("You run headless: the process ends when this turn ends. Never start anything in the background or wait for a notification. ")
	b.WriteString("Run only quick synchronous commands (grep, ls, a single test file); never the whole suite — judge suite-level criteria by the tests that exist and their content. ")
	b.WriteString("Printing the TASK lines is the last thing you do and it is mandatory; a turn without them fails the task even when the code is right.\n\n")
	for index, criterion := range criteria {
		b.WriteString(fmt.Sprintf("Criterion %d: %s", index+1, criterion.Text))
		if criterion.Proof != "" {
			b.WriteString(" (proof: `" + criterion.Proof + "`)")
		}
		switch {
		case criterion.Proof == "" || index >= len(proofs):
			b.WriteString(" — no proof; judge it by reading")
		case proofs[index].Pass:
			b.WriteString(" — proof run by the conductor: passed")
		default:
			b.WriteString(" — proof run by the conductor: failed")
		}
		b.WriteString("\n")
	}
	return b.String()
}

var verifierLine = regexp.MustCompile(`(?m)^\s*TASK\s+([0-9]+)\s*:\s*(DONE|INCOMPLETE)\b\s*(?:[—:-]+\s*(.*))?$`)
var verifierEnvironmentObjection = regexp.MustCompile(`(?i)sandbox|could not run|cannot run|unable to run|not permitted|permission|unverified|could not verify|cannot verify|unable to verify|not verified|no network`)

// Verifier parses the read-only verifier's answer: exactly one line per
// criterion, every one DONE. Zero lines, a wrong count or any INCOMPLETE
// fails; the missing pieces go into the detail.
func Verifier(output string, criteria int, proofs []Verdict) Verdict {
	verdict := Verdict{Name: "verifier", Pass: false}
	matches := verifierLine.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		verdict.Signal = "the verifier printed no TASK n: DONE|INCOMPLETE lines"
		verdict.Detail = bound(tail(output))
		return verdict
	}
	seen := map[int]bool{}
	var incomplete []string
	var environmentObjections []string
	for _, match := range matches {
		number, _ := strconv.Atoi(match[1])
		seen[number] = true
		if match[2] == "INCOMPLETE" {
			what := strings.TrimSpace(match[3])
			if what == "" {
				what = "no detail given"
			}
			if number > 0 && number <= len(proofs) && proofs[number-1].Pass && verifierEnvironmentObjection.MatchString(what) {
				environmentObjections = append(environmentObjections, fmt.Sprintf("criterion %d: environment objection set aside, its proof passed — %s", number, what))
				continue
			}
			incomplete = append(incomplete, fmt.Sprintf("TASK %d: INCOMPLETE — %s", number, what))
		}
	}
	if len(seen) != criteria {
		verdict.Signal = fmt.Sprintf("the verifier answered %d of %d criteria", len(seen), criteria)
		verdict.Detail = bound(tail(output))
		return verdict
	}
	for number := 1; number <= criteria; number++ {
		if !seen[number] {
			verdict.Signal = fmt.Sprintf("the verifier skipped criterion %d", number)
			verdict.Detail = bound(tail(output))
			return verdict
		}
	}
	if len(incomplete) > 0 {
		verdict.Signal = fmt.Sprintf("%d criterion(s) INCOMPLETE", len(incomplete))
		verdict.Detail = strings.Join(append(incomplete, environmentObjections...), "\n")
		return verdict
	}
	verdict.Pass = true
	verdict.Signal = fmt.Sprintf("%d/%d DONE", criteria, criteria)
	if len(environmentObjections) > 0 {
		verdict.Signal += fmt.Sprintf(" (%d environment objection(s) set aside)", len(environmentObjections))
		verdict.Detail = strings.Join(environmentObjections, "\n")
	}
	return verdict
}

// NeedsVerifier says when gate 3 dispatches the independent verifier:
// high or critical lanes, a silent tree, or any retry.
func NeedsVerifier(complexity string, treeSilent bool, execution int) bool {
	return complexity == "high" || complexity == "critical" || treeSilent || execution > 1
}

// Decide fills Passed from the individual verdicts.
func (r *Report) Decide() {
	r.Passed = r.Finished.Pass && r.Tree.Pass && r.Tests.Pass && r.Scope.Pass
	for _, proof := range r.Proofs {
		r.Passed = r.Passed && proof.Pass
	}
	if r.Verifier != nil {
		r.Passed = r.Passed && r.Verifier.Pass
	}
}

func tail(output string) string {
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if len(lines) > tailLines {
		lines = lines[len(lines)-tailLines:]
	}
	return strings.Join(lines, "\n")
}

func bound(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > detailLimit {
		return "…" + value[len(value)-detailLimit:]
	}
	return value
}

func indent(value string) string {
	lines := strings.Split(value, "\n")
	for index := range lines {
		lines[index] = "    " + lines[index]
	}
	return strings.Join(lines, "\n")
}

// SortedUnique is a small helper for path lists in verdicts.
func SortedUnique(values []string) []string {
	values = slices.Clone(values)
	slices.Sort(values)
	return slices.Compact(values)
}
