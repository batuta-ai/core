package executor

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"time"
)

const (
	// CapabilityMarker is the line a capable executor prints after the probe
	// command ran.
	CapabilityMarker = "BATUTA-CAPABLE"

	probeTailLines = 10
	probeTailBytes = 2048

	// The probe brief stays one paragraph and never names the task.
	probeBrief = "Run the shell command `git status --porcelain && echo ok` in the current directory. " +
		"Do not create, edit or delete any file. " +
		"When the command has run, print the line " + CapabilityMarker + " on a line of its own and stop."
)

// Probe failure reasons.
const (
	ProbeNoMarker    = "no_marker"
	ProbeUnfinished  = "unfinished"
	ProbeTimeout     = "timeout"
	ProbeLimit       = "limit"
	ProbeTreeChanged = "tree_changed"
)

// ProbeRoute is the part of a route the probe varies besides the adapter:
// model and effort fill the adapter's placeholders.
type ProbeRoute struct {
	Model  string
	Effort string
}

// ProbeResult is the verdict of one capability probe.
type ProbeResult struct {
	Pass     bool
	Reason   string // empty when Pass
	ExitCode int
	Duration time.Duration
	Tail     string // bounded tail of the executor output
}

// ProbeCapability asks the adapter's executor to run a command in dir and
// reports whether it did. The `git status --porcelain` of dir must be the
// same afterwards; a dir that is not a git work tree skips that check. A
// command that could not start is an error, not a verdict.
func ProbeCapability(ctx context.Context, backend Backend, adapter Adapter, route ProbeRoute, dir string, timeout time.Duration) (ProbeResult, error) {
	request := Request{Brief: probeBrief, Cwd: dir, Model: route.Model, Effort: route.Effort}
	invocation, err := adapter.Command(request)
	if err != nil {
		return ProbeResult{}, err
	}
	before, tracked := porcelainStatus(ctx, dir)
	result, err := backend.Execute(ctx, Execution{Adapter: adapter, Request: request, Invocation: invocation, Timeout: timeout})
	if err != nil {
		return ProbeResult{}, err
	}
	probe := ProbeResult{ExitCode: result.ExitCode, Duration: result.Duration, Tail: probeTail(result)}
	switch {
	case result.TimedOut:
		probe.Reason = ProbeTimeout
	case result.RateLimited:
		probe.Reason = ProbeLimit
	case !result.Finished:
		probe.Reason = ProbeUnfinished
	case tracked && statusChanged(ctx, dir, before):
		probe.Reason = ProbeTreeChanged
	case !hasMarkerLine(result.Stdout):
		probe.Reason = ProbeNoMarker
	default:
		probe.Pass = true
	}
	return probe, nil
}

func porcelainStatus(ctx context.Context, dir string) (string, bool) {
	command := exec.CommandContext(ctx, "git", "status", "--porcelain")
	command.Dir = dir
	var out bytes.Buffer
	command.Stdout = &out
	if err := command.Run(); err != nil {
		return "", false
	}
	return out.String(), true
}

func statusChanged(ctx context.Context, dir, before string) bool {
	after, ok := porcelainStatus(ctx, dir)
	return !ok || after != before
}

func hasMarkerLine(stdout []byte) bool {
	for _, line := range strings.Split(string(stdout), "\n") {
		if strings.TrimSpace(line) == CapabilityMarker {
			return true
		}
	}
	return false
}

func probeTail(result Result) string {
	tail := DropSecretLines(Tail(result.Stdout, probeTailLines))
	if tail == "" {
		tail = DropSecretLines(Tail(result.Stderr, probeTailLines))
	}
	if len(tail) > probeTailBytes {
		tail = tail[len(tail)-probeTailBytes:]
	}
	return tail
}
