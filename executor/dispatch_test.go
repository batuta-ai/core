package executor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/core/publication"
)

type dispatchBackendFunc func(context.Context, Execution) (Result, error)

func (f dispatchBackendFunc) Execute(ctx context.Context, e Execution) (Result, error) {
	return f(ctx, e)
}

func dispatchTestOptions(t *testing.T, backend Backend) DispatchOptions {
	t.Helper()
	return DispatchOptions{
		Adapter:   Adapter{Name: "fixture", Run: fmt.Sprintf("%q {brief}", os.Args[0])},
		Request:   Request{Brief: "private brief", Cwd: t.TempDir(), Model: "selected", Effort: "high"},
		Transport: TransportBackend{Mode: "auto", CLI: backend}, Timeout: time.Minute,
	}
}

func TestDispatchCancellationRetainsIntentAndNeverReplays(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	opts := dispatchTestOptions(t, dispatchBackendFunc(func(_ context.Context, e Execution) (Result, error) {
		calls++
		dir := filepath.Dir(e.Request.BriefFile)
		payload, err := os.ReadFile(filepath.Join(dir, "intent.json"))
		if err != nil {
			t.Fatal(err)
		}
		var intent struct {
			RunID      string          `json:"run_id"`
			Digest     string          `json:"brief_sha256"`
			Submission SubmissionState `json:"submission"`
			Model      string          `json:"model"`
			Effort     string          `json:"effort"`
			Cwd        string          `json:"cwd"`
		}
		if err := json.Unmarshal(payload, &intent); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256([]byte("private brief"))
		if intent.RunID == "" || intent.Digest != fmt.Sprintf("%x", digest) || intent.Submission != SubmissionUncertain || intent.Model != "selected" || intent.Effort != "high" || intent.Cwd != e.Request.Cwd {
			t.Fatalf("intent missing before execution: %s", payload)
		}
		if _, err := io.WriteString(e.Stdout, "partial evidence\n"); err != nil {
			t.Fatal(err)
		}
		cancel()
		// A late success after interruption must not erase uncertainty.
		return Result{ExitCode: 0, Finished: true}, nil
	}))
	report, _ := Dispatch(ctx, opts)
	t.Cleanup(func() { os.RemoveAll(report.Artifacts.Directory) })
	if calls != 1 || report.ExitClass != "uncertain" || report.ExitCode != 130 || report.Receipt.Submission.State != SubmissionUncertain {
		t.Fatalf("interrupted attempt: calls=%d report=%+v", calls, report)
	}
	log, err := os.ReadFile(filepath.Join(report.Artifacts.Directory, report.Artifacts.Stdout))
	if err != nil || string(log) != "partial evidence\n" {
		t.Fatalf("lost evidence: %q, %v", log, err)
	}
	var saved DispatchReport
	payload, err := os.ReadFile(filepath.Join(report.Artifacts.Directory, report.Artifacts.Receipt))
	if err != nil || json.Unmarshal(payload, &saved) != nil || saved.ExitClass != "uncertain" {
		t.Fatalf("lost receipt: %s, %v", payload, err)
	}
}

func TestDispatchPreflightCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opts := dispatchTestOptions(t, dispatchBackendFunc(func(ctx context.Context, _ Execution) (Result, error) {
		cancel()
		return Result{}, ctx.Err()
	}))
	opts.Preflight = true
	report, _ := Dispatch(ctx, opts)
	t.Cleanup(func() { os.RemoveAll(report.Artifacts.Directory) })
	if report.ExitClass != "uncertain" || report.ExitCode != 130 {
		t.Fatalf("canceled preflight: class=%q code=%d", report.ExitClass, report.ExitCode)
	}
}

func TestDispatchPreflightCanceledAfterBackendSuccess(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	opts := dispatchTestOptions(t, dispatchBackendFunc(func(context.Context, Execution) (Result, error) {
		calls++
		cancel()
		return Result{ExitCode: 0, Finished: true}, nil
	}))
	opts.Preflight = true
	report, err := Dispatch(ctx, opts)
	t.Cleanup(func() { os.RemoveAll(report.Artifacts.Directory) })
	if !errors.Is(err, context.Canceled) || calls != 1 || report.ExitClass != "uncertain" || report.ExitCode != 130 || report.Preflight != nil {
		t.Fatalf("canceled preflight: calls=%d report=%+v err=%v", calls, report, err)
	}
}

func TestDispatchPreflightUnavailable(t *testing.T) {
	opts := dispatchTestOptions(t, dispatchBackendFunc(func(context.Context, Execution) (Result, error) {
		return Result{}, errors.New("cannot start")
	}))
	opts.Preflight = true
	report, _ := Dispatch(context.Background(), opts)
	t.Cleanup(func() { os.RemoveAll(report.Artifacts.Directory) })
	if report.ExitClass != "unavailable" || report.ExitCode != 2 {
		t.Fatalf("unstartable preflight: class=%q code=%d", report.ExitClass, report.ExitCode)
	}
}

func TestDispatchTaskTimeoutExits124AcrossTransports(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	for _, mode := range []string{"cli", "auto", "acp"} {
		t.Run(mode, func(t *testing.T) {
			transport, execution, calls := transportFixture(t)
			execution.Adapter.Run = fmt.Sprintf("%q {brief}", executable)
			transport.Mode = mode
			transport.CLI = CLIBackend{Subprocess: Subprocess{
				Lookup: func(name string) (string, error) { return filepath.Join(execution.Request.Cwd, name), nil },
				Runner: backendCommandRunner(func(ctx context.Context, _ publication.Command) (publication.CommandResult, error) {
					*calls++
					<-ctx.Done()
					return publication.CommandResult{ExitCode: -1}, ctx.Err()
				}),
			}}
			if mode != "cli" {
				peerBackend := backendPeer(t, execution, func(reader *bufio.Reader, peer net.Conn) {
					backendReply(peer, backendRead(t, reader, "initialize"), `{"protocolVersion":1,"agentCapabilities":{}}`)
					config := `{"sessionId":"task"}`
					if mode == "acp" {
						config = `{"sessionId":"task","configOptions":[{"id":"m","category":"model","type":"select","currentValue":"model","options":[{"value":"model"}]},{"id":"e","category":"thought_level","type":"select","currentValue":"medium","options":[{"value":"medium"}]}]}`
					}
					backendReply(peer, backendRead(t, reader, "session/new"), config)
					io.Copy(io.Discard, reader)
				})
				transport.ACP.Open = peerBackend.Open
			}
			opts := DispatchOptions{Adapter: execution.Adapter, Request: execution.Request, Transport: transport, Timeout: 100 * time.Millisecond}
			report, _ := Dispatch(context.Background(), opts)
			t.Cleanup(func() { os.RemoveAll(report.Artifacts.Directory) })
			wantCalls := 0
			if mode != "acp" {
				wantCalls = 1
			}
			if report.ExitClass != "uncertain" || report.ExitCode != 124 || report.Receipt.Transport.Outcome == TransportCanceled || *calls != wantCalls {
				t.Fatalf("timeout dispatch: report=%+v calls=%d", report, *calls)
			}
		})
	}
}

func TestDispatchOwnsUniqueBoundedArtifacts(t *testing.T) {
	opts := dispatchTestOptions(t, dispatchBackendFunc(func(_ context.Context, e Execution) (Result, error) {
		for _, out := range []io.Writer{e.Stdout, e.Stderr} {
			if _, err := out.Write(bytes.Repeat([]byte("x"), outputLimit+17)); err != nil {
				t.Fatal(err)
			}
		}
		return Result{ExitCode: 0, Finished: true}, nil
	}))
	directories := map[string]bool{}
	for i := 0; i < 2; i++ {
		report, err := Dispatch(context.Background(), opts)
		if err != nil {
			t.Fatal(err)
		}
		dir := report.Artifacts.Directory
		t.Cleanup(func() { os.RemoveAll(dir) })
		if directories[dir] || !report.Truncated {
			t.Fatalf("reused directory or hid truncation: %+v", report)
		}
		directories[dir] = true
		info, err := os.Stat(dir)
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("directory permissions: %v %v", info, err)
		}
		for _, name := range []string{report.Artifacts.Stdout, report.Artifacts.Stderr} {
			info, err := os.Stat(filepath.Join(dir, name))
			if err != nil || info.Size() != 8<<20 {
				t.Fatalf("unbounded log: %v %v", info, err)
			}
		}
	}
}

func TestDispatchDoesNotFollowReplacedArtifact(t *testing.T) {
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := dispatchTestOptions(t, dispatchBackendFunc(func(_ context.Context, e Execution) (Result, error) {
		path := filepath.Join(filepath.Dir(e.Request.BriefFile), "receipt.json")
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(victim, path); err != nil {
			t.Fatal(err)
		}
		return Result{ExitCode: 0, Finished: true}, nil
	}))
	report, err := Dispatch(context.Background(), opts)
	t.Cleanup(func() { os.RemoveAll(report.Artifacts.Directory) })
	if err == nil || report.ExitClass != "uncertain" {
		t.Fatalf("artifact substitution reported success: %+v %v", report, err)
	}
	got, err := os.ReadFile(victim)
	if err != nil || string(got) != "keep" {
		t.Fatalf("overwrote arbitrary path: %q %v", got, err)
	}
}

func TestDispatchReportCompactionPreservesFailureAndEvidence(t *testing.T) {
	report := DispatchReport{ExitClass: "uncertain", ExitCode: 5,
		Receipt: Receipt{Submission: Submission{State: SubmissionUncertain}, Transport: Transport{Outcome: TransportDisconnected}, Worker: WorkerClaim{Outcome: WorkerClaimUnknown, Detail: string(bytes.Repeat([]byte("x"), 8192))}, Evidence: &ArtifactReference{Path: "/owned/evidence.json"}}}
	payload, err := marshalDispatchReport(report)
	if err != nil || len(payload)+1 > ReceiptLimit {
		t.Fatalf("unbounded report: %d %v", len(payload), err)
	}
	var got DispatchReport
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Receipt.Overflow || got.Receipt.OmittedBytes != 8192 || got.Receipt.Evidence.Path != "/owned/evidence.json" || got.ExitClass != "uncertain" || got.Receipt.Submission.State != SubmissionUncertain {
		t.Fatalf("compaction lost facts: %s", payload)
	}
}

func TestDispatchPreflightEscapingFitsReceipt(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		output          string
		retainedDetails []string
	}{
		{"quotes", strings.Repeat(`"`, probeTailBytes), nil},
		{"backslashes", strings.Repeat(`\`, probeTailBytes), nil},
		{"controls", strings.Repeat("\t\r", probeTailBytes/2), nil},
		{"html", strings.Repeat("<>&", probeTailBytes), nil},
		{"unicode", strings.Repeat("<&\\\"\t界", probeTailBytes/4), nil},
		{"invalid utf8", string(bytes.Repeat([]byte{0xff}, 1000)), nil},
		{"mixed invalid utf8", strings.Repeat("<", 700) + "diagnostic before invalid: 界\n" + string(bytes.Repeat([]byte{0xff}, 1000)) + "\ndiagnostic after invalid: <&界", []string{"diagnostic before invalid: 界", "diagnostic after invalid: <&界"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := Result{Stdout: []byte(tt.output), ExitCode: 9, Finished: true}
			opts := dispatchTestOptions(t, dispatchBackendFunc(func(context.Context, Execution) (Result, error) {
				return result, nil
			}))
			opts.Request.Cwd = tempDir(t)
			opts.Preflight = true
			report, err := Dispatch(context.Background(), opts)
			t.Cleanup(func() { os.RemoveAll(report.Artifacts.Directory) })
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(report.Artifacts.Directory, report.Artifacts.Receipt)
			payload, err := os.ReadFile(path)
			if err != nil || len(payload) > ReceiptLimit || len(payload) == 0 || payload[len(payload)-1] != '\n' {
				t.Fatalf("receipt size = %d, limit = %d, err = %v", len(payload), ReceiptLimit, err)
			}
			var saved DispatchReport
			if err := json.Unmarshal(payload, &saved); err != nil {
				t.Fatal(err)
			}
			evidence, err := os.ReadFile(filepath.Join(report.Artifacts.Directory, report.Artifacts.Evidence))
			if err != nil {
				t.Fatal(err)
			}
			var full DispatchReport
			if err := json.Unmarshal(evidence, &full); err != nil {
				t.Fatal(err)
			}
			if full.Preflight == nil || full.Preflight.Tail != string([]rune(probeTail(result))) || saved.Preflight == nil || saved.Preflight.Reason != ProbeNoRepository || saved.Preflight.Pass || saved.ExitClass != "executor_incapable" || saved.ExitCode != 6 || saved.WorkerExitCode != -1 || saved.RequestedTransport != "auto" || saved.Receipt.Submission.State != SubmissionNotSubmitted || saved.Receipt.Transport.Outcome != TransportNotStarted || saved.Receipt.Worker.Outcome != WorkerClaimUnknown {
				t.Fatalf("failure facts changed: saved=%+v full=%+v", saved, full)
			}
			if !saved.Receipt.Overflow || saved.Receipt.OmittedBytes < 0 || saved.Receipt.OmittedBytes != len(full.Preflight.Tail)-len(saved.Preflight.Tail) || !strings.HasSuffix(full.Preflight.Tail, saved.Preflight.Tail) || saved.Receipt.Evidence == nil || saved.Receipt.Evidence.Path != filepath.Join(report.Artifacts.Directory, report.Artifacts.Evidence) || report.Preflight.Tail != saved.Preflight.Tail {
				t.Fatalf("compaction or evidence mismatch: saved=%+v full=%+v", saved, full)
			}
			if len(tt.retainedDetails) > 0 && saved.Receipt.OmittedBytes == 0 {
				t.Fatalf("mixed malformed tail was not compacted: %+v", saved)
			}
			for _, detail := range tt.retainedDetails {
				if !strings.Contains(saved.Preflight.Tail, detail) {
					t.Fatalf("lost neighboring diagnostic %q: %q", detail, saved.Preflight.Tail)
				}
			}
		})
	}
}

func TestDispatchCompactedPassingPreflightKeepsWorkerFailure(t *testing.T) {
	t.Parallel()
	dir := gitRepo(t)
	head := probeHead(t, dir)
	calls := 0
	opts := dispatchTestOptions(t, dispatchBackendFunc(func(context.Context, Execution) (Result, error) {
		calls++
		if calls == 1 {
			return Result{Stdout: []byte(strings.Repeat("<", probeTailBytes) + "\n" + CapabilityMarker + " " + head + "\n"), Finished: true}, nil
		}
		return Result{ExitCode: 17}, nil
	}))
	opts.Request.Cwd = dir
	opts.Preflight = true
	report, err := Dispatch(context.Background(), opts)
	t.Cleanup(func() { os.RemoveAll(report.Artifacts.Directory) })
	if err != nil || calls != 2 {
		t.Fatalf("dispatch calls=%d err=%v", calls, err)
	}
	payload, err := os.ReadFile(filepath.Join(report.Artifacts.Directory, report.Artifacts.Receipt))
	if err != nil || len(payload) > ReceiptLimit {
		t.Fatalf("receipt size=%d err=%v", len(payload), err)
	}
	if report.Preflight == nil || !report.Preflight.Pass || report.Preflight.Reason != "" || report.ExitClass != "failed" || report.ExitCode != 1 || report.WorkerExitCode != 17 || report.Backend != "cli" || report.Receipt.Submission.State != SubmissionSubmitted || report.Receipt.Transport.Outcome != TransportCompleted || report.Receipt.Worker.Outcome != WorkerClaimedFailure || !report.Receipt.Overflow || report.Receipt.OmittedBytes == 0 {
		t.Fatalf("worker or preflight facts changed: %+v", report)
	}
}
