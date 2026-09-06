package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/batuta-ai/core/publication"
)

const (
	outputLimit    = 8 << 20
	limitTailLines = 20
	// QuestionPrefix is the line an executor prints (last line of its
	// output) when a stop condition needs the human: the loop parks the
	// task and relays the text. The brief states the protocol verbatim.
	QuestionPrefix = "BATUTA-QUESTION:"
)

// Result is what one executor run produced.
type Result struct {
	ExitCode    int
	Stdout      []byte
	Stderr      []byte
	Truncated   bool
	TimedOut    bool
	Duration    time.Duration
	Finished    bool      // gate 0: the executor ended on its own terms
	RateLimited bool      // the adapter's limit_regex matched the output tail
	ResetAt     time.Time // when the limit lifts, if the output said; zero otherwise
	Question    string    // a BATUTA-QUESTION line, when the executor asked one
	Progress    []ProgressEvent
}

// Subprocess runs invocations through the publication runner, resolving
// the executable to an absolute path first.
type Subprocess struct {
	Runner      publication.CommandRunner
	Lookup      func(string) (string, error)
	Environment []string
	Progress    func(ProgressEvent)
}

// NewSubprocess is the production runner: exec through PATH.
func NewSubprocess() Subprocess {
	return Subprocess{Runner: publication.ExecRunner{}, Lookup: exec.LookPath}
}

// Execute runs the invocation with stdin closed and returns its result. A
// non-zero exit is a result, not an error; only a command that could not
// start is an error.
func (s Subprocess) Execute(ctx context.Context, adapter Adapter, invocation Invocation, timeout time.Duration) (Result, error) {
	executable, err := s.resolve(invocation.Executable)
	if err != nil {
		return Result{ExitCode: -1}, err
	}
	runCtx := ctx
	cancel := context.CancelFunc(func() {})
	if timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()
	started := time.Now()
	sink := &progressSink{callback: s.Progress}
	stdoutObserver := &progressObserver{sink: sink}
	stderrObserver := &progressObserver{sink: sink}
	environment, err := unsignedGitEnvironment(s.Environment)
	if err != nil {
		return Result{ExitCode: -1}, err
	}
	raw, runErr := s.Runner.Run(runCtx, publication.Command{
		Executable: executable, Args: invocation.Args, Directory: invocation.Dir,
		Environment: environment, StdoutLimit: outputLimit, StderrLimit: outputLimit,
		Observer: stdoutObserver, StderrObserver: stderrObserver,
	})
	stdoutObserver.flush()
	stderrObserver.flush()
	result := Result{
		ExitCode: raw.ExitCode, Stdout: raw.Stdout, Stderr: raw.Stderr,
		Truncated: raw.StdoutTruncated || raw.StderrTruncated, Duration: time.Since(started),
		Progress: sink.events,
	}
	if runErr != nil {
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			result.TimedOut = true
			result.ExitCode = -1
		} else if ctx.Err() != nil {
			return result, ctx.Err()
		} else if raw.ExitCode < 0 {
			return result, fmt.Errorf("executor: %s did not start: %w", filepath.Base(executable), runErr)
		}
	}
	adapter.Outcome(&result)
	return result, nil
}

func unsignedGitEnvironment(environment []string) ([]string, error) {
	count := 0
	if value, found := os.LookupEnv("GIT_CONFIG_COUNT"); found {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 {
			return nil, fmt.Errorf("executor: invalid GIT_CONFIG_COUNT %q", value)
		}
		count = parsed
	}
	for _, entry := range environment {
		name, value, found := strings.Cut(entry, "=")
		if found && name == "GIT_CONFIG_COUNT" {
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed < 0 {
				return nil, fmt.Errorf("executor: invalid GIT_CONFIG_COUNT %q", value)
			}
			count = parsed
		}
	}

	result := append([]string(nil), environment...)
	return append(result,
		"GIT_CONFIG_COUNT="+strconv.Itoa(count+1),
		"GIT_CONFIG_KEY_"+strconv.Itoa(count)+"=commit.gpgsign",
		"GIT_CONFIG_VALUE_"+strconv.Itoa(count)+"=false",
	), nil
}

func (s Subprocess) resolve(executable string) (string, error) {
	if strings.TrimSpace(executable) == "" {
		return "", errors.New("executor: empty executable")
	}
	if strings.ContainsRune(executable, filepath.Separator) {
		abs, err := filepath.Abs(executable)
		if err != nil {
			return "", err
		}
		return abs, nil
	}
	lookup := s.Lookup
	if lookup == nil {
		lookup = exec.LookPath
	}
	path, err := lookup(executable)
	if err != nil {
		return "", fmt.Errorf("executor: %s is not on PATH", executable)
	}
	return filepath.Abs(path)
}

// Outcome applies the adapter's `finished` rule and `limit_regex` to a
// result and extracts a question line when present. `finished: exit_code`
// is the rule every shipped adapter uses; an unknown rule falls back to it.
func (a Adapter) Outcome(result *Result) {
	result.Finished = result.ExitCode == 0 && !result.TimedOut
	// The limit message comes at the END of a run. Matching the whole
	// output lets a project's own test output ("429", "Too Many Requests")
	// masquerade as a limit, so only the tail counts.
	output := Tail(result.Stdout, limitTailLines) + "\n" + Tail(result.Stderr, limitTailLines)
	if a.LimitRegex != "" {
		if pattern, err := regexp.Compile("(?i)" + a.LimitRegex); err == nil && pattern.MatchString(output) {
			result.RateLimited = true
			result.ResetAt = ResetTime(output, time.Now())
		}
	}
	if question, asked := Question(result.Stdout); asked {
		result.Question = question
	}
}

// Question returns the text of the last BATUTA-QUESTION line in an output.
func Question(stdout []byte) (string, bool) {
	lines := strings.Split(string(stdout), "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		line := strings.TrimSpace(lines[index])
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, QuestionPrefix) {
			text := strings.TrimSpace(strings.TrimPrefix(line, QuestionPrefix))
			return text, text != ""
		}
		return "", false
	}
	return "", false
}

// Tail returns the last n lines of an output, for feedback and trails.
func Tail(payload []byte, n int) string {
	lines := strings.Split(strings.TrimRight(string(payload), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

var (
	resetEpoch = regexp.MustCompile(`(?i)(?:usage limit reached|reset[a-z ]*)[^0-9]*([0-9]{10,13})`)
	resetClock = regexp.MustCompile(`(?i)resets?\s+(?:at\s+)?([0-9]{1,2})(?::([0-9]{2}))?\s*(am|pm)`)
)

// ResetTime reads the reset moment a usage-limit message may carry: a raw
// epoch (seconds or milliseconds) or a clock time such as "resets 11:10am",
// resolved to its next occurrence after now. Zero when the output names
// none.
func ResetTime(output string, now time.Time) time.Time {
	if match := resetEpoch.FindStringSubmatch(output); match != nil {
		var epoch int64
		for _, digit := range match[1] {
			epoch = epoch*10 + int64(digit-'0')
		}
		if len(match[1]) >= 13 {
			epoch /= 1000
		}
		return time.Unix(epoch, 0).UTC()
	}
	if match := resetClock.FindStringSubmatch(output); match != nil {
		hour, minute := 0, 0
		for _, digit := range match[1] {
			hour = hour*10 + int(digit-'0')
		}
		for _, digit := range match[2] {
			minute = minute*10 + int(digit-'0')
		}
		if strings.EqualFold(match[3], "pm") && hour < 12 {
			hour += 12
		}
		if strings.EqualFold(match[3], "am") && hour == 12 {
			hour = 0
		}
		local := now.Local()
		reset := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, local.Location())
		if !reset.After(local) {
			reset = reset.Add(24 * time.Hour)
		}
		return reset
	}
	return time.Time{}
}
