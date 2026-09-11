package executor

import (
	"context"
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

// ACPLaunch is optional adapter metadata, never qualification evidence. Version
// is the exact trimmed --version output of the separately installed executable.
// Codex and Claude require their dedicated wrappers; nothing installs them.
type ACPLaunch struct {
	Run            string
	Version        string
	ModelConfigID  string
	EffortConfigID string
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
// for that exact launch, version, platform, model and effort. Adapter frontmatter
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

// TransportBackend selects transport per execution without changing provider or
// route. Zero Mode means CLI. Qualifications has no default approved entries.
// ACP.Open must own a qualified lifecycle; the current acp.Process implementation
// reports unresolved descendant cleanup and must not receive a qualified record.
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

func (b TransportBackend) Execute(ctx context.Context, e Execution) (Result, error) {
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
		runCtx, cancel = context.WithTimeout(ctx, e.Timeout)
		defer cancel()
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
	result, err := backend.Execute(runCtx, e)
	var incompatible *acpCompatibilityError
	if b.Mode == "auto" && runCtx.Err() == nil && errors.As(err, &incompatible) {
		return cli.Execute(runCtx, e)
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
		if q.Executor == e.Adapter.Name && q.Run == e.Adapter.ACP.Run && q.Version == e.Adapter.ACP.Version && q.GOOS == runtime.GOOS && q.GOARCH == runtime.GOARCH && q.Model == e.Request.Model && q.Effort == e.Request.Effort && q.Permissions && q.Cleanup && q.AuthenticatedTask && q.Platform {
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
