package executor

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// DispatchOptions describes one external attempt. There is no retry or resume.
type DispatchOptions struct {
	Adapter   Adapter
	Request   Request
	Transport TransportBackend
	Timeout   time.Duration
}

// DispatchArtifacts names files relative to the private directory. Keep these
// files for reconciliation and run acceptance gates independently.
type DispatchArtifacts struct {
	Directory string `json:"directory"`
	Brief     string `json:"brief"`
	Intent    string `json:"intent"`
	Receipt   string `json:"receipt"`
	Evidence  string `json:"evidence"`
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
}

// DispatchReport contains execution facts, never a verified acceptance verdict.
type DispatchReport struct {
	RunID              string            `json:"run_id,omitempty"`
	Executor           string            `json:"executor,omitempty"`
	Model              string            `json:"model,omitempty"`
	Effort             string            `json:"effort,omitempty"`
	RequestedTransport string            `json:"requested_transport,omitempty"`
	Backend            string            `json:"backend,omitempty"`
	ExitClass          string            `json:"exit_class"`
	ExitCode           int               `json:"exit_code"`
	WorkerExitCode     int               `json:"worker_exit_code"`
	Truncated          bool              `json:"truncated,omitempty"`
	Receipt            Receipt           `json:"receipt"`
	Artifacts          DispatchArtifacts `json:"artifacts"`
}

func ValidateTransport(mode string) error {
	if mode != "cli" && mode != "acp" && mode != "auto" {
		return errors.New("transport must be cli, acp or auto; native tools belong to the interactive host")
	}
	return nil
}

func validateDispatch(opts DispatchOptions) error {
	if err := ValidateTransport(opts.Transport.Mode); err != nil {
		return err
	}
	if !adapterName.MatchString(opts.Adapter.Name) || opts.Adapter.IsSelf() || opts.Adapter.Name == "native" {
		return errors.New("dispatch requires an external executor")
	}
	if !dispatchID(opts.Request.Model) || opts.Request.Model == DefaultModel || (opts.Request.Effort != "" && !dispatchID(opts.Request.Effort)) {
		return errors.New("dispatch requires an explicit model and valid model/effort identifiers")
	}
	if opts.Timeout <= 0 || len(opts.Request.Brief) > 1<<20 || strings.TrimSpace(opts.Request.Brief) == "" {
		return errors.New("dispatch requires a positive timeout and a nonempty brief of at most 1 MiB")
	}
	info, err := os.Stat(opts.Request.Cwd)
	if err != nil || !filepath.IsAbs(opts.Request.Cwd) || !info.IsDir() {
		return errors.New("dispatch requires an existing absolute working directory")
	}
	return nil
}

func dispatchID(value string) bool {
	if value == "" || len(value) > 256 || !strings.ContainsAny(value[:1], "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789") {
		return false
	}
	for _, r := range value {
		if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._:/@+-", r) {
			return false
		}
	}
	return true
}

// Dispatch retains an intent before execution and owns every artifact descriptor
// before giving control to the worker. Replaced paths cannot redirect our writes.
func Dispatch(ctx context.Context, opts DispatchOptions) (report DispatchReport, err error) {
	report.ExitClass, report.ExitCode, report.WorkerExitCode = "invalid_arguments", 2, -1
	report.Receipt = Receipt{Submission: Submission{State: SubmissionNotSubmitted}, Transport: Transport{Outcome: TransportNotStarted}, Worker: WorkerClaim{Outcome: WorkerClaimUnknown}}
	if opts.Transport.Mode == "" {
		opts.Transport.Mode = "cli"
	}
	if err := validateDispatch(opts); err != nil {
		return report, err
	}
	dir, err := os.MkdirTemp("", "batuta-dispatch-")
	if err != nil {
		return report, err
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return report, err
	}
	report.RunID = filepath.Base(dir)
	report.Executor, report.Model, report.Effort = opts.Adapter.Name, opts.Request.Model, opts.Request.Effort
	report.RequestedTransport = opts.Transport.Mode
	report.Artifacts = DispatchArtifacts{dir, "brief.md", "intent.json", "receipt.json", "evidence.json", "stdout.log", "stderr.log"}
	files := make(map[string]*os.File)
	defer func() {
		for _, f := range files {
			err = errors.Join(err, f.Close())
		}
	}()
	for _, name := range []string{"brief.md", "intent.json", "receipt.json", "evidence.json", "stdout.log", "stderr.log"} {
		f, openErr := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if openErr != nil {
			return report, openErr
		}
		files[name] = f
	}
	if _, err := io.WriteString(files["brief.md"], opts.Request.Brief); err != nil {
		return report, err
	}
	opts.Request.BriefFile = filepath.Join(dir, "brief.md")
	invocation, err := opts.Adapter.Command(opts.Request)
	if err != nil {
		return report, err
	}
	digest := sha256.Sum256([]byte(opts.Request.Brief))
	intent := struct {
		RunID       string          `json:"run_id"`
		Executor    string          `json:"executor"`
		Model       string          `json:"model"`
		Effort      string          `json:"effort,omitempty"`
		Cwd         string          `json:"cwd"`
		Transport   string          `json:"requested_transport"`
		BriefDigest string          `json:"brief_sha256"`
		Submission  SubmissionState `json:"submission"`
	}{report.RunID, report.Executor, report.Model, report.Effort, opts.Request.Cwd, opts.Transport.Mode, fmt.Sprintf("%x", digest), SubmissionUncertain}
	if err := json.NewEncoder(files["intent.json"]).Encode(intent); err != nil {
		return report, err
	}
	for _, name := range []string{"brief.md", "intent.json"} {
		if err := files[name].Sync(); err != nil {
			return report, err
		}
	}
	if _, err := marshalDispatchReport(report); err != nil {
		return report, err
	}
	stdout, stderr := &dispatchLog{file: files["stdout.log"]}, &dispatchLog{file: files["stderr.log"]}
	transport := opts.Transport
	cli := transport.CLI
	if cli == nil {
		cli = CLIBackend{Subprocess: NewSubprocess()}
	}
	transport.CLI = dispatchCLI{backend: cli, selected: func() { report.Backend = "cli" }}
	if transport.Mode == "acp" {
		report.Backend = "acp"
	}
	result, execErr := transport.Execute(ctx, Execution{Adapter: opts.Adapter, Request: opts.Request, Invocation: invocation, Timeout: opts.Timeout, Stdout: stdout, Stderr: stderr})
	if result.Receipt != nil {
		report.Receipt = *result.Receipt
	}
	if report.Backend == "" && report.Receipt.Transport.Outcome != TransportNotStarted {
		report.Backend = "acp"
	}
	report.WorkerExitCode = result.ExitCode
	report.Truncated = result.Truncated || stdout.truncated || stderr.truncated
	report.ExitClass, report.ExitCode = dispatchExit(ctx, result, report.Receipt, execErr)
	if stdout.err != nil || stderr.err != nil {
		report.ExitClass, report.ExitCode = "uncertain", 5
	}
	if err := dispatchArtifactIdentity(dir, files); err != nil {
		report.ExitClass, report.ExitCode = "uncertain", 5
		return report, errors.Join(execErr, err)
	}
	report.Receipt.Evidence = &ArtifactReference{Path: filepath.Join(dir, "evidence.json")}
	if err := json.NewEncoder(files["evidence.json"]).Encode(report); err != nil {
		return report, errors.Join(execErr, err)
	}
	payload, err := marshalDispatchReport(report)
	if err != nil {
		return report, errors.Join(execErr, err)
	}
	// Return the same compact report persisted on disk.
	if err := json.Unmarshal(payload, &report); err != nil {
		return report, err
	}
	if _, err := files["receipt.json"].Write(append(payload, '\n')); err != nil {
		return report, errors.Join(execErr, err)
	}
	for _, f := range files {
		if err := f.Sync(); err != nil {
			return report, errors.Join(execErr, err)
		}
	}
	return report, errors.Join(execErr, stdout.err, stderr.err)
}

type dispatchCLI struct {
	backend  Backend
	selected func()
}

func (b dispatchCLI) Execute(ctx context.Context, e Execution) (Result, error) {
	b.selected()
	resolved, err := exec.LookPath(e.Invocation.Executable)
	if err != nil {
		return unavailableTransport(err)
	}
	if err := ctx.Err(); err != nil {
		return unavailableTransport(err)
	}
	e.Invocation.Executable = resolved
	result, err := b.backend.Execute(ctx, e)
	result.Receipt = &Receipt{Submission: Submission{State: SubmissionSubmitted}, Transport: Transport{Outcome: TransportCompleted}, Worker: WorkerClaim{Outcome: WorkerClaimedFailure}}
	if result.Finished {
		result.Receipt.Worker.Outcome = WorkerClaimedSuccess
	}
	if err != nil || result.TimedOut || ctx.Err() != nil {
		result.Receipt.Submission.State = SubmissionUncertain
		result.Receipt.Transport.Outcome = TransportFailed
		result.Receipt.Worker.Outcome = WorkerClaimUnknown
		if ctx.Err() != nil {
			result.Receipt.Transport.Outcome = TransportCanceled
		}
	}
	return result, err
}

func dispatchArtifactIdentity(dir string, files map[string]*os.File) error {
	for name, file := range files {
		opened, err := file.Stat()
		if err != nil {
			return err
		}
		current, err := os.Lstat(filepath.Join(dir, name))
		if err != nil || !current.Mode().IsRegular() || !os.SameFile(opened, current) {
			return errors.New("dispatch: owned artifact was replaced; retain evidence for reconciliation")
		}
	}
	return nil
}

func dispatchExit(ctx context.Context, result Result, receipt Receipt, err error) (string, int) {
	if ctx.Err() != nil {
		return "uncertain", 130
	}
	if result.TimedOut {
		return "uncertain", 124
	}
	if receipt.Submission.State == SubmissionNotSubmitted {
		return "unavailable", 2
	}
	if err != nil || receipt.Submission.State != SubmissionSubmitted || receipt.Transport.Outcome != TransportCompleted {
		return "uncertain", 5
	}
	if result.Question != "" {
		return "waiting_input", 3
	}
	if result.RateLimited {
		return "rate_limited", 4
	}
	if !result.Finished || result.ExitCode != 0 {
		return "failed", 1
	}
	return "completed", 0
}

func marshalDispatchReport(report DispatchReport) ([]byte, error) {
	payload, err := json.Marshal(report)
	if err != nil || len(payload)+1 <= ReceiptLimit {
		return payload, err
	}
	report.Receipt.OmittedBytes += len(report.Receipt.Worker.Detail)
	report.Receipt.Worker.Detail = ""
	report.Receipt.Overflow = true
	payload, err = json.Marshal(report)
	if err == nil && len(payload)+1 > ReceiptLimit {
		err = errors.New("dispatch: essential report exceeds receipt limit")
	}
	return payload, err
}

type dispatchLog struct {
	file      *os.File
	written   int
	truncated bool
	err       error
}

func (w *dispatchLog) Write(p []byte) (int, error) {
	n := min(len(p), outputLimit-w.written)
	w.truncated = w.truncated || n < len(p)
	if w.err != nil {
		return 0, w.err
	}
	written, err := w.file.Write(p[:n])
	w.written += written
	w.err = err
	if err != nil {
		return written, err
	}
	return len(p), nil
}
