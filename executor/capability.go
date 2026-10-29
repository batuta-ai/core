package executor

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/batuta-ai/core/publication"
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
	Receipt  *Receipt
}

// ProbeCapability asks the adapter's executor to run a command in dir and
// reports whether it did. The marker must carry the HEAD of dir, and the
// worktree contents and HEAD must remain unchanged. A command that could not
// start is an error, not a verdict.
func ProbeCapability(ctx context.Context, backend Backend, adapter Adapter, route ProbeRoute, dir string, timeout time.Duration) (ProbeResult, error) {
	request := Request{Brief: probeBrief, Cwd: dir, Model: route.Model, Effort: route.Effort}
	invocation, err := adapter.Command(request)
	if err != nil {
		return ProbeResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return ProbeResult{}, err
	}
	before, beforeErr := probeWorktreeState(ctx, dir)
	if err := ctx.Err(); err != nil {
		return ProbeResult{}, err
	}
	result, err := backend.Execute(ctx, Execution{Adapter: adapter, Request: request, Invocation: invocation, Timeout: timeout})
	probe := ProbeResult{ExitCode: result.ExitCode, Duration: result.Duration, Tail: probeTail(result), Receipt: result.Receipt}
	if ctxErr := ctx.Err(); ctxErr != nil {
		if probe.Receipt == nil {
			return ProbeResult{}, ctxErr
		}
		return probe, ctxErr
	}
	if err != nil {
		return probe, err
	}
	var after publication.WorktreeState
	var afterErr error
	if beforeErr == nil {
		after, afterErr = probeWorktreeState(ctx, dir)
	}
	if err := ctx.Err(); err != nil {
		if probe.Receipt == nil {
			return ProbeResult{}, err
		}
		return probe, err
	}
	switch {
	case beforeErr != nil:
		probe.Reason = ProbeNoRepository
	case afterErr != nil || after != before:
		probe.Reason = ProbeTreeChanged
	case result.TimedOut:
		probe.Reason = ProbeTimeout
	case result.RateLimited:
		probe.Reason = ProbeLimit
	case !result.Finished:
		probe.Reason = ProbeUnfinished
	default:
		probe.Reason = markerReason(result.Stdout, before.HeadSHA)
		probe.Pass = probe.Reason == ""
	}
	if err := ctx.Err(); err != nil {
		if probe.Receipt == nil {
			return ProbeResult{}, err
		}
		return probe, err
	}
	return probe, nil
}

func probeWorktreeState(ctx context.Context, dir string) (publication.WorktreeState, error) {
	git, err := exec.LookPath("git")
	if err != nil {
		return publication.WorktreeState{}, err
	}
	git, err = filepath.Abs(git)
	if err != nil {
		return publication.WorktreeState{}, err
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return publication.WorktreeState{}, err
	}
	return (publication.GitClient{Executable: git, Runner: publication.ExecRunner{}}).WorktreeState(ctx, dir)
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
	var tail string
	for _, source := range [][]byte{result.Stdout, result.Stderr, result.RawStdout} {
		tail = DropSecretBearingLines(Tail(source, probeTailLines))
		if tail != "" {
			break
		}
	}
	if len(tail) > probeTailBytes {
		tail = strings.ToValidUTF8(tail[len(tail)-probeTailBytes:], "")
	}
	return tail
}
