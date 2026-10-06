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
	probeBrief = "Run the shell command `git rev-parse HEAD` in the current directory. " +
		"Do not create, edit or delete any file. " +
		"When the command has run, print " + CapabilityMarker + " <sha> on a line of its own, replacing <sha> with the command's output, and stop."
)

// Probe failure reasons.
const (
	ProbeNoMarker       = "no_marker"
	ProbeMarkerMismatch = "marker_mismatch"
	ProbeNoRepository   = "no_repository"
	ProbeUnfinished     = "unfinished"
	ProbeTimeout        = "timeout"
	ProbeLimit          = "limit"
	ProbeTreeChanged    = "tree_changed"
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
// reports whether it did. The marker must carry the HEAD of dir, and the
// worktree status and HEAD must remain unchanged. A command that could not
// start is an error, not a verdict.
func ProbeCapability(ctx context.Context, backend Backend, adapter Adapter, route ProbeRoute, dir string, timeout time.Duration) (ProbeResult, error) {
	request := Request{Brief: probeBrief, Cwd: dir, Model: route.Model, Effort: route.Effort}
	invocation, err := adapter.Command(request)
	if err != nil {
		return ProbeResult{}, err
	}
	before, tracked := porcelainStatus(ctx, dir)
	head, hasHead := headSHA(ctx, dir)
	result, err := backend.Execute(ctx, Execution{Adapter: adapter, Request: request, Invocation: invocation, Timeout: timeout})
	if err != nil {
		return ProbeResult{}, err
	}
	probe := ProbeResult{ExitCode: result.ExitCode, Duration: result.Duration, Tail: probeTail(result)}
	switch {
	case !hasHead || !tracked:
		probe.Reason = ProbeNoRepository
	case statusChanged(ctx, dir, before) || headChanged(ctx, dir, head):
		probe.Reason = ProbeTreeChanged
	case result.TimedOut:
		probe.Reason = ProbeTimeout
	case result.RateLimited:
		probe.Reason = ProbeLimit
	case !result.Finished:
		probe.Reason = ProbeUnfinished
	default:
		probe.Reason = markerReason(result.Stdout, head)
		probe.Pass = probe.Reason == ""
	}
	return probe, nil
}

func headSHA(ctx context.Context, dir string) (string, bool) {
	command := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	command.Dir = dir
	var out bytes.Buffer
	command.Stdout = &out
	if err := command.Run(); err != nil {
		return "", false
	}
	return strings.TrimSpace(out.String()), true
}

func headChanged(ctx context.Context, dir, before string) bool {
	after, ok := headSHA(ctx, dir)
	return !ok || after != before
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

func markerReason(stdout []byte, head string) string {
	found := false
	for _, line := range strings.Split(string(stdout), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != CapabilityMarker {
			continue
		}
		found = true
		if len(fields) == 2 && fields[1] == head {
			return ""
		}
	}
	if found {
		return ProbeMarkerMismatch
	}
	return ProbeNoMarker
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
