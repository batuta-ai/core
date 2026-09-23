package executor

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/batuta-ai/core/executor/acp"
	"github.com/batuta-ai/core/publication"
)

var ErrACPUnavailable = errors.New("executor: ACP prerequisites or qualification unavailable")

var errTaskTimeout = errors.New("executor: task timeout")

// ACPLaunch is optional adapter metadata, never qualification evidence. Version
// is the exact trimmed --version output of the separately installed executable.
// Codex and Claude require their dedicated wrappers; nothing installs them.
type ACPLaunch struct {
	Run            string
	Version        string
	ModelConfigID  string
	EffortConfigID string
	Mode           string
	SessionMeta    json.RawMessage
}

func (a Adapter) acpCommand() (Invocation, error) {
	if a.ACP == nil || strings.TrimSpace(a.ACP.Version) == "" {
		return Invocation{}, ErrACPUnavailable
	}
	argv, err := Tokenize(a.ACP.Run)
	if err != nil {
		return Invocation{}, ErrACPUnavailable
	}
	var executable string
	var args string
	switch a.Name {
	case "codex":
		executable = "codex-acp"
	case "claude":
		executable = "claude-agent-acp"
	case "opencode":
		executable, args = "opencode", "acp"
	case "cursor-agent":
		executable, args = "cursor-agent", "acp"
	default:
		return Invocation{}, ErrACPUnavailable
	}
	// Launches are fixed argv, without prompt placeholders, auth or install hooks.
	wantArgs := 1
	if args != "" {
		wantArgs++
	}
	if len(argv) != wantArgs || filepath.Base(argv[0]) != executable || strings.Join(argv[1:], " ") != args || strings.Contains(a.ACP.Run, "<") {
		return Invocation{}, ErrACPUnavailable
	}
	return Invocation{Executable: argv[0], Args: argv[1:]}, nil
}

// ACPQualification is trusted release evidence supplied by the owner of ACP.Open,
// for that exact launch, version, platform, model and effort. Model "*" matches
// any requested model, and an empty qualification Effort matches any requested effort
// when the adapter declares no acp_effort_config; executor, run, version,
// platform and lifecycle flags still must match exactly. Adapter frontmatter
// cannot grant eligibility. No provider is universally qualified: OpenCode and
// Cursor remain pilots; Codex permission/cleanup and Claude authenticated task
// evidence are gates. Native Windows tests are required for a Windows record.
type ACPQualification struct {
	Executor          string
	Run               string
	Version           string
	GOOS              string
	GOARCH            string
	Model             string
	Effort            string
	Permissions       bool
	Cleanup           bool
	AuthenticatedTask bool
	Platform          bool
}

func (q ACPQualification) matches(e Execution) bool {
	if e.Adapter.ACP == nil {
		return false
	}
	if q.Executor != e.Adapter.Name || q.Run != e.Adapter.ACP.Run || q.Version != e.Adapter.ACP.Version {
		return false
	}
	if q.GOOS != runtime.GOOS || q.GOARCH != runtime.GOARCH {
		return false
	}
	if !q.Permissions || !q.Cleanup || !q.AuthenticatedTask || !q.Platform {
		return false
	}
	if q.Model != "*" && q.Model != e.Request.Model {
		return false
	}
	if q.Effort == e.Request.Effort {
		return true
	}
	return q.Effort == "" && e.Adapter.ACP.EffortConfigID == ""
}

// TransportBackend selects transport per execution without changing provider or
// route. Zero Mode means CLI. Qualifications has no default approved entries.
// ACP.Open must own a qualified lifecycle; acp.Process verifies a managed process
// group, not arbitrary descendant containment. Provider qualification still
// requires separate native lifecycle and authenticated task evidence.
// Open receives the resolved ACP command in Execution.Invocation. CLI always
// receives the original invocation. Session negotiation verifies actual protocol
// and selected model/effort before the single task prompt; no model probe runs.
type TransportBackend struct {
	Mode           string
	CLI            Backend
	ACP            ACPBackend
	Qualifications []ACPQualification
	Lookup         func(string) (string, error)
	VersionRunner  publication.CommandRunner
}

func (b TransportBackend) Execute(ctx context.Context, e Execution) (result Result, err error) {
	cli := b.CLI
	if cli == nil {
		cli = CLIBackend{Subprocess: NewSubprocess()}
	}
	switch b.Mode {
	case "", "cli":
		return cli.Execute(ctx, e)
	case "acp", "auto":
	default:
		return unavailableTransport(errors.New("executor: unknown transport"))
	}
	runCtx := ctx
	if e.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeoutCause(ctx, e.Timeout, errTaskTimeout)
		defer cancel()
		defer func() {
			if errors.Is(context.Cause(runCtx), errTaskTimeout) {
				result, err = taskTimedOut(result, err)
			}
		}()
	}
	if err := runCtx.Err(); err != nil {
		return unavailableTransport(err)
	}
	launch, err := b.prepare(runCtx, e)
	if err != nil {
		if runCtx.Err() != nil {
			return unavailableTransport(runCtx.Err())
		}
		if b.Mode == "auto" {
			return cli.Execute(runCtx, e)
		}
		return unavailableTransport(err)
	}
	backend := b.ACP
	backend.ModelConfigID = e.Adapter.ACP.ModelConfigID
	backend.EffortConfigID = e.Adapter.ACP.EffortConfigID
	open := backend.Open
	backend.Open = func(ctx context.Context, execution Execution) (*acp.Connection, func() error, error) {
		execution.Invocation = launch
		return open(ctx, execution)
	}
	result, err = backend.Execute(runCtx, e)
	var incompatible *acpCompatibilityError
	if b.Mode == "auto" && runCtx.Err() == nil && errors.As(err, &incompatible) {
		return cli.Execute(runCtx, e)
	}
	return result, err
}

func taskTimedOut(result Result, err error) (Result, error) {
	result.ExitCode = -1
	result.Finished = false
	result.TimedOut = true
	if result.Receipt != nil {
		if result.Receipt.Submission.State != SubmissionNotSubmitted {
			result.Receipt.Submission.State = SubmissionUncertain
		}
		if result.Receipt.Transport.Failure != "shutdown" {
			result.Receipt.Transport = Transport{Outcome: TransportFailed, Failure: "timeout"}
		}
		result.Receipt.Worker.Outcome = WorkerClaimUnknown
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		err = errors.Join(context.DeadlineExceeded, err)
	}
	return result, err
}

func (b TransportBackend) prepare(ctx context.Context, e Execution) (Invocation, error) {
	launch, err := e.Adapter.acpCommand()
	if err != nil || e.Adapter.IsSelf() || b.ACP.Open == nil || b.ACP.PermissionPolicy == nil {
		return Invocation{}, ErrACPUnavailable
	}
	qualified := false
	for _, q := range b.Qualifications {
		if q.matches(e) {
			qualified = true
			break
		}
	}
	if !qualified {
		return Invocation{}, ErrACPUnavailable
	}
	lookup := b.Lookup
	if lookup == nil {
		lookup = exec.LookPath
	}
	resolved, err := lookup(launch.Executable)
	if err != nil || !filepath.IsAbs(resolved) {
		return Invocation{}, ErrACPUnavailable
	}
	runner := b.VersionRunner
	if runner == nil {
		runner = publication.ExecRunner{}
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	observed, err := runner.Run(probeCtx, publication.Command{Executable: resolved, Args: []string{"--version"}, Directory: e.Request.Cwd, StdoutLimit: 4096, StderrLimit: 4096})
	if err != nil || observed.ExitCode != 0 || observed.StdoutTruncated || observed.StderrTruncated || strings.TrimSpace(string(observed.Stdout)) != e.Adapter.ACP.Version {
		return Invocation{}, ErrACPUnavailable
	}
	launch.Executable, launch.Dir = resolved, e.Request.Cwd
	return launch, nil
}

func unavailableTransport(err error) (Result, error) {
	return Result{ExitCode: -1, Receipt: &Receipt{Submission: Submission{State: SubmissionNotSubmitted}, Transport: Transport{Outcome: TransportNotStarted, Failure: "configuration"}, Worker: WorkerClaim{Outcome: WorkerClaimUnknown}}}, err
}
