package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/core/executor/acp"
	"github.com/batuta-ai/core/publication"
)

func transportFixture(t *testing.T) (TransportBackend, Execution, *int) {
	t.Helper()
	adapter, err := ParseAdapter([]byte(strings.Replace(codexAdapter, "name: codex", "name: codex\nacp_run: codex-acp\nacp_version: 1.11.0", 1)))
	if err != nil {
		t.Fatal(err)
	}
	execution := Execution{Adapter: adapter, Request: Request{Cwd: t.TempDir(), Brief: "task", Model: "model", Effort: "medium"}, Invocation: Invocation{Executable: "codex", Args: []string{"exec", "task"}}}
	calls := new(int)
	backend := TransportBackend{Mode: "auto", CLI: CLIBackend{Subprocess: Subprocess{
		Lookup: func(name string) (string, error) { return filepath.Join(execution.Request.Cwd, name), nil },
		Runner: backendCommandRunner(func(_ context.Context, command publication.Command) (publication.CommandResult, error) {
			*calls++
			if filepath.Base(command.Executable) != "codex" || strings.Join(command.Args, " ") != "exec task" {
				t.Errorf("CLI changed: %+v", command)
			}
			return publication.CommandResult{Stdout: []byte("legacy")}, nil
		}),
	}}, Lookup: func(name string) (string, error) { return filepath.Join(execution.Request.Cwd, name), nil },
		VersionRunner: backendCommandRunner(func(_ context.Context, command publication.Command) (publication.CommandResult, error) {
			if filepath.Base(command.Executable) != "codex-acp" || strings.Join(command.Args, " ") != "--version" || len(command.Stdin) != 0 || command.StdoutLimit != 4096 {
				t.Errorf("probe: %+v", command)
			}
			return publication.CommandResult{Stdout: []byte("1.11.0\n")}, nil
		}),
		Qualifications: []ACPQualification{{Executor: "codex", Run: "codex-acp", Version: "1.11.0", GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Model: "model", Effort: "medium", Permissions: true, Cleanup: true, AuthenticatedTask: true, Platform: true}},
	}
	backend.ACP.PermissionPolicy = func(context.Context, Execution, acp.PermissionRequest) string { return "" }
	backend.ACP.Open = func(context.Context, Execution) (*acp.Connection, func() error, error) {
		t.Error("unexpected ACP launch")
		return nil, nil, errors.New("unexpected")
	}
	return backend, execution, calls
}

func TestTransportKeepsCLISelectable(t *testing.T) {
	for _, mode := range []string{"", "cli"} {
		t.Run(mode, func(t *testing.T) {
			backend, execution, calls := transportFixture(t)
			backend.Mode = mode
			backend.Lookup = func(string) (string, error) { t.Fatal("CLI checked ACP availability"); return "", nil }
			result, err := backend.Execute(context.Background(), execution)
			if err != nil || !result.Finished || string(result.Stdout) != "legacy" || *calls != 1 {
				t.Fatalf("result: %+v / %v calls=%d", result, err, *calls)
			}
		})
	}
}

func TestNativeTransportRequiresReleaseQualification(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"", "cli", "auto", "acp"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			fixture, execution, calls := transportFixture(t)
			backend := NewNativeTransport(mode)
			backend.CLI = fixture.CLI
			backend.Lookup = func(string) (string, error) {
				t.Fatal("unqualified native launch attempted discovery")
				return "", nil
			}
			if backend.Mode != mode || backend.ACP.Open == nil || backend.ACP.PermissionPolicy == nil {
				t.Fatal("native constructor lost policy, ownership or qualification boundary")
			}
			permission := acp.PermissionRequest{Options: []acp.PermissionOption{{OptionID: "yes", Kind: "allow_always"}}}
			if choice := backend.ACP.PermissionPolicy(context.Background(), execution, permission); choice != "" {
				t.Fatalf("default policy granted permission: %q", choice)
			}
			result, err := backend.Execute(context.Background(), execution)
			if mode == "acp" {
				if !errors.Is(err, ErrACPUnavailable) || *calls != 0 || result.Receipt.Submission.State != SubmissionNotSubmitted {
					t.Fatalf("unqualified native launch: %+v / %v calls=%d", result, err, *calls)
				}
			} else if err != nil || *calls != 1 || !result.Finished || string(result.Stdout) != "legacy" {
				t.Fatalf("CLI behavior changed: %+v / %v calls=%d", result, err, *calls)
			}
		})
	}
}

func TestNativeTransportExactReleaseSelection(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name          string
		syntheticHost bool
		wantLookup    bool
		wantProbe     bool
		wantACP       bool
	}{
		{name: "qualified", wantLookup: true, wantProbe: true, wantACP: true},
		{name: "synthetic host qualification", syntheticHost: true, wantLookup: true, wantProbe: true, wantACP: true},
		{name: "version", syntheticHost: true},
		{name: "model", syntheticHost: true, wantLookup: true, wantProbe: true, wantACP: true},
		{name: "effort", syntheticHost: true, wantLookup: true, wantProbe: true, wantACP: true},
		{name: "launch", syntheticHost: true},
		{name: "codex", syntheticHost: true},
		{name: "claude", syntheticHost: true},
		{name: "cursor-agent", syntheticHost: true},
		{name: "agy", syntheticHost: true},
		{name: "missing", syntheticHost: true, wantLookup: true},
		{name: "observed version", syntheticHost: true, wantLookup: true, wantProbe: true},
		{name: "version error", syntheticHost: true, wantLookup: true, wantProbe: true},
	} {
		for _, mode := range []string{"", "cli", "acp", "auto"} {
			t.Run(scenario.name+"/"+mode, func(t *testing.T) {
				t.Parallel()
				fixture, execution, calls := transportFixture(t)
				execution.Adapter.Name = "opencode"
				execution.Adapter.ACP = &ACPLaunch{Run: "opencode acp", Version: "1.18.31", ModelConfigID: "model"}
				execution.Request.Model, execution.Request.Effort = "opencode/big-pickle", ""
				backend := NewNativeTransport(mode)
				wantQualification := ACPQualification{
					Executor: "opencode", Run: "opencode acp", Version: "1.18.31",
					GOOS: "darwin", GOARCH: "arm64", Model: "*", Effort: "",
					Permissions: true, Cleanup: true, AuthenticatedTask: true, Platform: true,
				}
				if len(backend.Qualifications) == 0 || !sameACPQualification(backend.Qualifications[0], wantQualification) {
					t.Fatalf("release qualification changed: %+v", backend.Qualifications)
				}
				if scenario.syntheticHost {
					// Exercise selection and probe paths on every CI host. This
					// test-owned copy is not native qualification evidence.
					qualification := backend.Qualifications[0]
					qualification.GOOS, qualification.GOARCH = runtime.GOOS, runtime.GOARCH
					backend.Qualifications = []ACPQualification{qualification}
				}
				switch scenario.name {
				case "version":
					execution.Adapter.ACP.Version = "1.18.32"
				case "model":
					execution.Request.Model = "opencode/other"
				case "effort":
					execution.Request.Effort = "high"
				case "launch":
					execution.Adapter.ACP.Run = filepath.Join(execution.Request.Cwd, "opencode") + " acp"
				case "codex", "claude", "cursor-agent", "agy":
					execution.Adapter.Name = scenario.name
					execution.Adapter.ACP.Run = map[string]string{"codex": "codex-acp", "claude": "claude-agent-acp", "cursor-agent": "cursor-agent acp", "agy": "agy acp"}[scenario.name]
				}
				backend.CLI = fixture.CLI
				lookups, probes, opens := 0, 0, 0
				resolved := filepath.Join(execution.Request.Cwd, "opencode")
				backend.Lookup = func(name string) (string, error) {
					lookups++
					if name != "opencode" {
						t.Errorf("unexpected discovery: %s", name)
					}
					if scenario.name == "missing" {
						return "", os.ErrNotExist
					}
					return resolved, nil
				}
				backend.VersionRunner = backendCommandRunner(func(_ context.Context, command publication.Command) (publication.CommandResult, error) {
					probes++
					if command.Executable != resolved || strings.Join(command.Args, " ") != "--version" || command.Directory != execution.Request.Cwd || len(command.Stdin) != 0 || command.StdoutLimit != 4096 || command.StderrLimit != 4096 {
						t.Errorf("version probe changed: %+v", command)
					}
					if scenario.name == "version error" {
						return publication.CommandResult{}, errors.New("version failed")
					}
					if scenario.name == "observed version" {
						return publication.CommandResult{Stdout: []byte("1.18.32\n")}, nil
					}
					return publication.CommandResult{Stdout: []byte("1.18.31\n")}, nil
				})
				peer := backendPeer(t, execution, func(reader *bufio.Reader, conn net.Conn) {
					backendReply(conn, backendRead(t, reader, "initialize"), `{"protocolVersion":1,"agentCapabilities":{}}`)
					backendReply(conn, backendRead(t, reader, "session/new"), fmt.Sprintf(`{"sessionId":"task","configOptions":[{"id":"model","category":"model","type":"select","currentValue":%q,"options":[{"value":%q}]}]}`, execution.Request.Model, execution.Request.Model))
					backendReply(conn, backendRead(t, reader, "session/prompt"), `{"stopReason":"end_turn"}`)
					io.Copy(io.Discard, reader)
				})
				backend.ACP.Open = func(ctx context.Context, got Execution) (*acp.Connection, func() error, error) {
					opens++
					if probes != 1 || got.Invocation.Executable != resolved || strings.Join(got.Invocation.Args, " ") != "acp" || got.Invocation.Dir != execution.Request.Cwd {
						t.Errorf("launch before exact version or with changed argv: %+v", got.Invocation)
					}
					return peer.Open(ctx, got)
				}
				result, err := backend.Execute(context.Background(), execution)
				eligible := (mode == "acp" || mode == "auto") && (scenario.syntheticHost || (runtime.GOOS == "darwin" && runtime.GOARCH == "arm64"))
				wantLookup := eligible && scenario.wantLookup
				wantProbe := eligible && scenario.wantProbe
				wantACP := eligible && scenario.wantACP
				wantCLI := mode == "" || mode == "cli" || (mode == "auto" && !wantACP)
				if lookups != boolCount(wantLookup) || probes != boolCount(wantProbe) || opens != boolCount(wantACP) || *calls != boolCount(wantCLI) {
					t.Fatalf("unexpected attempts: lookup=%d version=%d ACP=%d CLI=%d", lookups, probes, opens, *calls)
				}
				if wantACP || wantCLI {
					if err != nil || !result.Finished || (wantACP && (result.Receipt == nil || result.Receipt.Submission.State != SubmissionSubmitted)) {
						t.Fatalf("selected transport failed: %+v / %v", result, err)
					}
				} else if !errors.Is(err, ErrACPUnavailable) || result.Finished || result.Receipt.Submission.State != SubmissionNotSubmitted {
					t.Fatalf("unqualified task submitted: %+v / %v", result, err)
				}
			})
		}
	}
}

func boolCount(value bool) int {
	if value {
		return 1
	}
	return 0
}

func TestNativeTransportBridgeRecords(t *testing.T) {
	t.Parallel()
	backend := NewNativeTransport("acp")
	want := []ACPQualification{
		{
			Executor: "opencode", Run: "opencode acp", Version: "1.18.31",
			GOOS: "darwin", GOARCH: "arm64", Model: "*", Effort: "",
			Permissions: true, Cleanup: true, AuthenticatedTask: true, Platform: true,
		},
		{
			Executor: "codex", Run: "codex-acp", Version: "@agentclientprotocol/codex-acp 1.13.1",
			GOOS: "darwin", GOARCH: "arm64", Model: "*", Effort: "", Mode: "read-only",
			Permissions: true, Cleanup: true, AuthenticatedTask: true, Platform: true,
		},
		{
			Executor: "claude", Run: "claude-agent-acp", Version: "0.81.1",
			GOOS: "darwin", GOARCH: "arm64", Model: "*", Effort: "", Mode: "acceptEdits",
			SessionMeta: json.RawMessage(`{"claudeCode":{"options":{"sandbox":{"enabled":true,"autoAllowBashIfSandboxed":true}}}}`),
			Permissions: true, Cleanup: true, AuthenticatedTask: true, Platform: true,
		},
	}
	if len(backend.Qualifications) != len(want) {
		t.Fatalf("release qualifications changed: %+v", backend.Qualifications)
	}
	for i := range want {
		if !sameACPQualification(backend.Qualifications[i], want[i]) {
			t.Fatalf("qualification %d: %+v", i, backend.Qualifications[i])
		}
	}
	codex := backend.Qualifications[1]
	codex.GOOS, codex.GOARCH = runtime.GOOS, runtime.GOARCH
	codexLaunch := &ACPLaunch{Run: "codex-acp", Version: "@agentclientprotocol/codex-acp 1.13.1"}
	codexExec := Execution{Adapter: Adapter{Name: "codex", ACP: codexLaunch}, Request: Request{Model: "gpt"}}
	if codex.matches(codexExec) {
		t.Fatal("codex without acp_mode is qualified")
	}
	claude := backend.Qualifications[2]
	claude.GOOS, claude.GOARCH = runtime.GOOS, runtime.GOARCH
	claudeLaunch := &ACPLaunch{Run: "claude-agent-acp", Version: "0.81.1", Mode: "acceptEdits"}
	claudeExec := Execution{Adapter: Adapter{Name: "claude", ACP: claudeLaunch}, Request: Request{Model: "haiku"}}
	if claude.matches(claudeExec) {
		t.Fatal("claude without session meta is qualified")
	}
}

func sameACPQualification(got, want ACPQualification) bool {
	return got.Executor == want.Executor && got.Run == want.Run && got.Version == want.Version &&
		got.GOOS == want.GOOS && got.GOARCH == want.GOARCH && got.Model == want.Model && got.Effort == want.Effort &&
		got.Mode == want.Mode && bytes.Equal(got.SessionMeta, want.SessionMeta) &&
		got.Permissions == want.Permissions && got.Cleanup == want.Cleanup &&
		got.AuthenticatedTask == want.AuthenticatedTask && got.Platform == want.Platform
}

func TestNativeTransportDefaultDenialNeverFallsBack(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"acp", "auto"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			fixture, execution, calls := transportFixture(t)
			backend := NewNativeTransport(mode)
			backend.CLI, backend.Lookup, backend.VersionRunner = fixture.CLI, fixture.Lookup, fixture.VersionRunner
			backend.Qualifications = fixture.Qualifications
			peer := backendPeer(t, execution, func(reader *bufio.Reader, conn net.Conn) {
				backendReply(conn, backendRead(t, reader, "initialize"), `{"protocolVersion":1,"agentCapabilities":{}}`)
				backendReply(conn, backendRead(t, reader, "session/new"), `{"sessionId":"task","configOptions":[{"id":"m","category":"model","type":"select","currentValue":"model","options":[{"value":"model"}]},{"id":"e","category":"thought_level","type":"select","currentValue":"medium","options":[{"value":"medium"}]}]}`)
				prompt := backendRead(t, reader, "session/prompt")
				io.WriteString(conn, `{"jsonrpc":"2.0","id":"permission","method":"session/request_permission","params":{"sessionId":"task","toolCall":{"toolCallId":"write"},"options":[{"optionId":"yes","kind":"allow_once"}]}}`+"\n")
				line, err := reader.ReadString('\n')
				if err != nil || !strings.Contains(line, `"cancelled"`) {
					t.Errorf("default policy response: %s / %v", line, err)
				}
				backendReply(conn, prompt, `{"stopReason":"end_turn"}`)
			})
			backend.ACP.Open = peer.Open
			result, err := backend.Execute(context.Background(), execution)
			if !errors.Is(err, acp.ErrPermissionDenied) || *calls != 0 || result.Finished || result.ExitCode == 0 || result.Receipt.Transport.Failure != "permission_denied" || result.Receipt.Worker.Outcome == WorkerClaimedSuccess {
				t.Fatalf("denied task replayed or succeeded: %+v / %v calls=%d", result, err, *calls)
			}
		})
	}
}

func TestTransportEligibilityFailsClosed(t *testing.T) {
	for _, reason := range []string{"legacy", "agy", "unknown", "unqualified", "version", "model", "effort", "os", "arch", "permissions", "cleanup", "task", "platform", "policy", "owner", "missing", "observed version"} {
		for _, mode := range []string{"acp", "auto"} {
			t.Run(reason+"/"+mode, func(t *testing.T) {
				backend, execution, calls := transportFixture(t)
				backend.Mode = mode
				switch reason {
				case "legacy":
					execution.Adapter.ACP = nil
				case "agy", "unknown":
					execution.Adapter.Name = reason
				case "unqualified":
					backend.Qualifications = nil
				case "version":
					backend.Qualifications[0].Version = "other"
				case "model":
					backend.Qualifications[0].Model = "other"
				case "effort":
					backend.Qualifications[0].Effort = "high"
				case "os":
					backend.Qualifications[0].GOOS = "other"
				case "arch":
					backend.Qualifications[0].GOARCH = "other"
				case "permissions":
					backend.Qualifications[0].Permissions = false
				case "cleanup":
					backend.Qualifications[0].Cleanup = false
				case "task":
					backend.Qualifications[0].AuthenticatedTask = false
				case "platform":
					backend.Qualifications[0].Platform = false
				case "policy":
					backend.ACP.PermissionPolicy = nil
				case "owner":
					backend.ACP.Open = nil
				case "missing":
					backend.Lookup = func(string) (string, error) { return "", os.ErrNotExist }
				case "observed version":
					backend.VersionRunner = backendCommandRunner(func(context.Context, publication.Command) (publication.CommandResult, error) {
						return publication.CommandResult{Stdout: []byte("other")}, nil
					})
				}
				result, err := backend.Execute(context.Background(), execution)
				if mode == "auto" {
					if err != nil || *calls != 1 || !result.Finished {
						t.Fatalf("auto: %+v / %v calls=%d", result, err, *calls)
					}
				} else if !errors.Is(err, ErrACPUnavailable) || *calls != 0 || result.Receipt == nil || result.Receipt.Submission.State != SubmissionNotSubmitted {
					t.Fatalf("explicit: %+v / %v calls=%d", result, err, *calls)
				}
			})
		}
	}
}

func TestTransportFallbackUsesSubmissionAndShutdownEvidence(t *testing.T) {
	for _, scenario := range []string{"protocol", "configuration", "shutdown", "open", "disconnect", "success"} {
		t.Run(scenario, func(t *testing.T) {
			backend, execution, calls := transportFixture(t)
			peerBackend := backendPeer(t, execution, func(reader *bufio.Reader, peer net.Conn) {
				version := `{"protocolVersion":1,"agentCapabilities":{}}`
				if scenario == "protocol" {
					version = `{"protocolVersion":99,"agentCapabilities":{}}`
				}
				backendReply(peer, backendRead(t, reader, "initialize"), version)
				if scenario == "protocol" {
					io.Copy(io.Discard, reader)
					return
				}
				config := `{"sessionId":"task"}`
				if scenario != "configuration" && scenario != "shutdown" {
					config = `{"sessionId":"task","configOptions":[{"id":"m","category":"model","type":"select","currentValue":"model","options":[{"value":"model"}]},{"id":"e","category":"thought_level","type":"select","currentValue":"medium","options":[{"value":"medium"}]}]}`
				}
				backendReply(peer, backendRead(t, reader, "session/new"), config)
				if scenario == "configuration" || scenario == "shutdown" {
					line, _ := reader.ReadBytes('\n')
					if len(line) > 0 {
						t.Errorf("prompt on incompatible config: %s", line)
					}
					return
				}
				request := backendRead(t, reader, "session/prompt")
				if scenario == "disconnect" {
					return
				}
				backendReply(peer, request, `{"stopReason":"end_turn"}`)
				io.Copy(io.Discard, reader)
			})
			backend.ACP.Open = peerBackend.Open
			if scenario == "shutdown" {
				open := backend.ACP.Open
				backend.ACP.Open = func(ctx context.Context, e Execution) (*acp.Connection, func() error, error) {
					conn, close, err := open(ctx, e)
					return conn, func() error { close(); return errors.New("unverified") }, err
				}
			}
			if scenario == "open" {
				backend.ACP.Open = func(context.Context, Execution) (*acp.Connection, func() error, error) {
					return nil, nil, errors.New("failed")
				}
			}
			result, err := backend.Execute(context.Background(), execution)
			fallback := scenario == "protocol" || scenario == "configuration"
			if (*calls == 1) != fallback {
				t.Fatalf("calls=%d result=%+v err=%v", *calls, result, err)
			}
			if fallback || scenario == "success" {
				if err != nil || !result.Finished {
					t.Fatalf("result=%+v err=%v", result, err)
				}
			} else if err == nil || result.Finished {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestTransportRejectsInvalidModesAndCanceledAttempts(t *testing.T) {
	for _, scenario := range []string{"mode", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			backend, execution, calls := transportFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if scenario == "mode" {
				backend.Mode = "native"
			} else {
				cancel()
			}
			backend.Lookup = func(string) (string, error) { t.Fatal("unexpected probe"); return "", nil }
			result, err := backend.Execute(ctx, execution)
			if err == nil || *calls != 0 || result.Finished || result.Receipt.Submission.State != SubmissionNotSubmitted {
				t.Fatalf("result=%+v err=%v calls=%d", result, err, *calls)
			}
		})
	}
}

func TestTransportPreservesTaskTimeoutClassification(t *testing.T) {
	for _, scenario := range []string{"cli", "auto fallback", "auto submitted", "acp"} {
		t.Run(scenario, func(t *testing.T) {
			backend, execution, calls := transportFixture(t)
			backend.Mode = strings.Fields(scenario)[0]
			execution.Timeout = 100 * time.Millisecond
			backend.CLI = CLIBackend{Subprocess: Subprocess{
				Lookup: func(name string) (string, error) { return filepath.Join(execution.Request.Cwd, name), nil },
				Runner: backendCommandRunner(func(ctx context.Context, _ publication.Command) (publication.CommandResult, error) {
					*calls++
					<-ctx.Done()
					return publication.CommandResult{ExitCode: -1}, ctx.Err()
				}),
			}}
			if scenario != "cli" {
				peerBackend := backendPeer(t, execution, func(reader *bufio.Reader, peer net.Conn) {
					backendReply(peer, backendRead(t, reader, "initialize"), `{"protocolVersion":1,"agentCapabilities":{}}`)
					config := `{"sessionId":"task"}`
					if scenario != "auto fallback" {
						config = `{"sessionId":"task","configOptions":[{"id":"m","category":"model","type":"select","currentValue":"model","options":[{"value":"model"}]},{"id":"e","category":"thought_level","type":"select","currentValue":"medium","options":[{"value":"medium"}]}]}`
					}
					backendReply(peer, backendRead(t, reader, "session/new"), config)
					io.Copy(io.Discard, reader)
				})
				backend.ACP.Open = peerBackend.Open
			}

			result, err := backend.Execute(context.Background(), execution)
			wantCalls := 0
			if scenario == "cli" || scenario == "auto fallback" {
				wantCalls = 1
			}
			if !result.TimedOut || result.Finished || (!errors.Is(err, context.DeadlineExceeded) && scenario != "cli") || *calls != wantCalls {
				t.Fatalf("timeout classification: result=%+v err=%v calls=%d", result, err, *calls)
			}
			if result.Receipt != nil && result.Receipt.Transport.Outcome == TransportCanceled {
				t.Fatalf("task timeout reported as caller cancellation: %+v", result.Receipt)
			}
			if scenario == "auto submitted" && result.Receipt.Submission.State != SubmissionUncertain {
				t.Fatalf("timed-out submission became replayable: %+v", result.Receipt)
			}
		})
	}
}

func TestTaskTimeoutPreservesUnverifiedShutdown(t *testing.T) {
	receipt := &Receipt{Submission: Submission{State: SubmissionSubmitted}, Transport: Transport{Outcome: TransportFailed, Failure: "shutdown"}, Worker: WorkerClaim{Outcome: WorkerClaimedSuccess}}
	result, err := taskTimedOut(Result{ExitCode: 0, Finished: true, Receipt: receipt}, errors.New("shutdown failed"))
	if !errors.Is(err, context.DeadlineExceeded) || !result.TimedOut || result.Finished || result.Receipt.Submission.State != SubmissionUncertain || result.Receipt.Transport.Failure != "shutdown" || result.Receipt.Worker.Outcome != WorkerClaimUnknown {
		t.Fatalf("timeout erased reconciliation evidence: result=%+v receipt=%+v err=%v", result, result.Receipt, err)
	}
}

func TestTransportVersionProbeIsBoundedAndFailClosed(t *testing.T) {
	for _, scenario := range []string{"error", "exit", "stdout overflow", "stderr overflow", "missing binary"} {
		t.Run(scenario, func(t *testing.T) {
			backend, execution, calls := transportFixture(t)
			backend.Mode = "acp"
			backend.VersionRunner = backendCommandRunner(func(ctx context.Context, command publication.Command) (publication.CommandResult, error) {
				if _, ok := ctx.Deadline(); !ok || command.StderrLimit != 4096 || command.Directory != execution.Request.Cwd {
					t.Errorf("unbounded probe: %+v", command)
				}
				result := publication.CommandResult{Stdout: []byte("1.11.0")}
				switch scenario {
				case "error":
					return result, errors.New("credential-canary")
				case "exit":
					result.ExitCode = 1
				case "stdout overflow":
					result.StdoutTruncated = true
				case "stderr overflow":
					result.StderrTruncated = true
				case "missing binary":
					t.Error("probe after unavailable executable")
				}
				return result, nil
			})
			if scenario == "missing binary" {
				backend.Lookup = nil
				execution.Adapter.ACP.Run = filepath.Join(t.TempDir(), "codex-acp")
				backend.Qualifications[0].Run = execution.Adapter.ACP.Run
			}
			result, err := backend.Execute(context.Background(), execution)
			if !errors.Is(err, ErrACPUnavailable) || strings.Contains(err.Error(), "canary") || *calls != 0 || result.Finished {
				t.Fatalf("result=%+v err=%v calls=%d", result, err, *calls)
			}
		})
	}
}

func TestQualificationEffortNotApplicable(t *testing.T) {
	t.Parallel()
	backend, execution, calls := transportFixture(t)
	backend.Mode = "acp"
	backend.Qualifications[0].Effort = ""
	execution.Request.Effort = "high"
	opens := 0
	peer := backendPeer(t, execution, func(reader *bufio.Reader, conn net.Conn) {
		backendReply(conn, backendRead(t, reader, "initialize"), `{"protocolVersion":1,"agentCapabilities":{}}`)
		backendReply(conn, backendRead(t, reader, "session/new"), `{"sessionId":"task","configOptions":[{"id":"m","category":"model","type":"select","currentValue":"model","options":[{"value":"model"}]}]}`)
		backendReply(conn, backendRead(t, reader, "session/prompt"), `{"stopReason":"end_turn"}`)
		io.Copy(io.Discard, reader)
	})
	backend.ACP.Open = func(ctx context.Context, got Execution) (*acp.Connection, func() error, error) {
		opens++
		return peer.Open(ctx, got)
	}
	result, err := backend.Execute(context.Background(), execution)
	if err != nil || *calls != 0 || opens != 1 || !result.Finished || result.Receipt == nil || result.Receipt.Submission.State != SubmissionSubmitted {
		t.Fatalf("effort blocked qualification: %+v / %v opens=%d calls=%d", result, err, opens, *calls)
	}
	if result.Receipt.Effort != "not_applicable" {
		t.Fatalf("effort disposition: %q", result.Receipt.Effort)
	}
}

func TestACPReceiptEffortSelectedByCategory(t *testing.T) {
	t.Parallel()
	backend, execution, calls := transportFixture(t)
	backend.Mode = "acp"
	backend.Qualifications[0].Effort = ""
	execution.Request.Effort = "high"
	opens := 0
	peer := backendPeer(t, execution, func(reader *bufio.Reader, conn net.Conn) {
		backendReply(conn, backendRead(t, reader, "initialize"), `{"protocolVersion":1,"agentCapabilities":{}}`)
		backendReply(conn, backendRead(t, reader, "session/new"), `{"sessionId":"task","configOptions":[{"id":"m","category":"model","type":"select","currentValue":"model","options":[{"value":"model"}]},{"id":"e","category":"thought_level","type":"select","currentValue":"high","options":[{"value":"high"}]}]}`)
		backendReply(conn, backendRead(t, reader, "session/prompt"), `{"stopReason":"end_turn"}`)
		io.Copy(io.Discard, reader)
	})
	backend.ACP.Open = func(ctx context.Context, got Execution) (*acp.Connection, func() error, error) {
		opens++
		return peer.Open(ctx, got)
	}
	result, err := backend.Execute(context.Background(), execution)
	if err != nil || *calls != 0 || opens != 1 || !result.Finished || result.Receipt == nil || result.Receipt.Submission.State != SubmissionSubmitted {
		t.Fatalf("selected effort: %+v / %v opens=%d calls=%d", result, err, opens, *calls)
	}
	if result.Receipt.Effort == "not_applicable" {
		t.Fatalf("effort disposition: %q", result.Receipt.Effort)
	}
}

func TestACPReceiptEffortRejectedNotStamped(t *testing.T) {
	t.Parallel()
	backend, execution, calls := transportFixture(t)
	backend.Mode = "acp"
	backend.Qualifications[0].Effort = ""
	execution.Request.Effort = "high"
	opens := 0
	peer := backendPeer(t, execution, func(reader *bufio.Reader, conn net.Conn) {
		backendReply(conn, backendRead(t, reader, "initialize"), `{"protocolVersion":1,"agentCapabilities":{}}`)
		backendReply(conn, backendRead(t, reader, "session/new"), `{"sessionId":"task","configOptions":[{"id":"m","category":"model","type":"select","currentValue":"model","options":[{"value":"model"}]},{"id":"e","category":"thought_level","type":"select","currentValue":"low","options":[{"value":"low"}]}]}`)
		line, _ := reader.ReadBytes('\n')
		if len(line) != 0 {
			t.Errorf("prompt after rejection: %s", line)
		}
	})
	backend.ACP.Open = func(ctx context.Context, got Execution) (*acp.Connection, func() error, error) {
		opens++
		return peer.Open(ctx, got)
	}
	result, err := backend.Execute(context.Background(), execution)
	if !errors.Is(err, acp.ErrConfiguration) || *calls != 0 || opens != 1 || result.Finished || result.Receipt == nil {
		t.Fatalf("rejected effort: %+v / %v opens=%d calls=%d", result, err, opens, *calls)
	}
	if result.Receipt.Effort == "not_applicable" {
		t.Fatalf("effort disposition: %q", result.Receipt.Effort)
	}
}

func TestQualificationAnyModel(t *testing.T) {
	t.Parallel()
	for _, mismatch := range []string{"match", "executor", "run", "version", "os", "arch", "permissions", "cleanup", "task", "platform"} {
		t.Run(mismatch, func(t *testing.T) {
			t.Parallel()
			backend, execution, calls := transportFixture(t)
			backend.Mode = "acp"
			backend.Qualifications[0].Model = "*"
			execution.Request.Model = "requested-model"
			switch mismatch {
			case "executor":
				backend.Qualifications[0].Executor = "other"
			case "run":
				backend.Qualifications[0].Run = "other-acp"
			case "version":
				backend.Qualifications[0].Version = "other"
			case "os":
				backend.Qualifications[0].GOOS = "other"
			case "arch":
				backend.Qualifications[0].GOARCH = "other"
			case "permissions":
				backend.Qualifications[0].Permissions = false
			case "cleanup":
				backend.Qualifications[0].Cleanup = false
			case "task":
				backend.Qualifications[0].AuthenticatedTask = false
			case "platform":
				backend.Qualifications[0].Platform = false
			}
			opens := 0
			peer := backendPeer(t, execution, func(reader *bufio.Reader, conn net.Conn) {
				backendReply(conn, backendRead(t, reader, "initialize"), `{"protocolVersion":1,"agentCapabilities":{}}`)
				backendReply(conn, backendRead(t, reader, "session/new"), `{"sessionId":"task","configOptions":[{"id":"m","category":"model","type":"select","currentValue":"requested-model","options":[{"value":"requested-model"}]},{"id":"e","category":"thought_level","type":"select","currentValue":"medium","options":[{"value":"medium"}]}]}`)
				backendReply(conn, backendRead(t, reader, "session/prompt"), `{"stopReason":"end_turn"}`)
				io.Copy(io.Discard, reader)
			})
			backend.ACP.Open = func(ctx context.Context, got Execution) (*acp.Connection, func() error, error) {
				opens++
				return peer.Open(ctx, got)
			}
			result, err := backend.Execute(context.Background(), execution)
			if mismatch == "match" {
				if err != nil || *calls != 0 || opens != 1 || !result.Finished || result.Receipt == nil || result.Receipt.Submission.State != SubmissionSubmitted {
					t.Fatalf("wildcard model: %+v / %v opens=%d calls=%d", result, err, opens, *calls)
				}
				return
			}
			if !errors.Is(err, ErrACPUnavailable) || *calls != 0 || opens != 0 || result.Finished || result.Receipt == nil || result.Receipt.Submission.State != SubmissionNotSubmitted {
				t.Fatalf("mismatch %s: %+v / %v opens=%d calls=%d", mismatch, result, err, opens, *calls)
			}
		})
	}
}

func TestQualificationPinsModeAndMeta(t *testing.T) {
	t.Parallel()
	pinned := ACPQualification{
		Executor: "codex", Run: "codex-acp", Version: "1.13.1",
		GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Model: "*", Effort: "",
		Mode: "read-only", SessionMeta: json.RawMessage(`{"sandbox":{"enabled":true}}`),
		Permissions: true, Cleanup: true, AuthenticatedTask: true, Platform: true,
	}
	execution := func(mode string, meta json.RawMessage) Execution {
		return Execution{
			Adapter: Adapter{Name: "codex", ACP: &ACPLaunch{Run: "codex-acp", Version: "1.13.1", Mode: mode, SessionMeta: meta}},
			Request: Request{Model: "any"},
		}
	}
	compact := json.RawMessage(`{"sandbox":{"enabled":true}}`)
	spaced := json.RawMessage(`{ "sandbox": { "enabled": true } }`)
	if !pinned.matches(execution("read-only", compact)) {
		t.Fatal("pinned mode and meta did not match")
	}
	if !pinned.matches(execution("read-only", spaced)) {
		t.Fatal("compacted session meta did not match")
	}
	if pinned.matches(execution("acceptEdits", compact)) {
		t.Fatal("mode mismatch qualified")
	}
	if pinned.matches(execution("", compact)) {
		t.Fatal("empty adapter mode matched pinned mode")
	}
	if pinned.matches(execution("read-only", nil)) {
		t.Fatal("empty adapter meta matched pinned meta")
	}
	if pinned.matches(execution("read-only", json.RawMessage(`{"sandbox":{"enabled":false}}`))) {
		t.Fatal("different session meta qualified")
	}
	empty := pinned
	empty.Mode, empty.SessionMeta = "", nil
	if !empty.matches(execution("", nil)) {
		t.Fatal("empty pins did not match adapter that declares none")
	}
	if empty.matches(execution("read-only", nil)) {
		t.Fatal("empty mode matched adapter mode")
	}
	if empty.matches(execution("", compact)) {
		t.Fatal("empty meta matched adapter meta")
	}
	if empty.matches(execution("", json.RawMessage(`{}`))) {
		t.Fatal("empty meta matched empty object")
	}
}

func TestTransportProviderIdentityAndResolvedLaunch(t *testing.T) {
	for _, provider := range []struct{ name, run string }{{"codex", "codex-acp"}, {"claude", "claude-agent-acp"}, {"opencode", "opencode acp"}, {"cursor-agent", "cursor-agent acp"}} {
		t.Run(provider.name, func(t *testing.T) {
			backend, execution, calls := transportFixture(t)
			execution.Adapter.Name = provider.name
			execution.Adapter.ACP.Run = provider.run
			backend.Qualifications[0].Executor = provider.name
			backend.Qualifications[0].Run = provider.run
			backend.VersionRunner = backendCommandRunner(func(context.Context, publication.Command) (publication.CommandResult, error) {
				return publication.CommandResult{Stdout: []byte("1.11.0")}, nil
			})
			peerBackend := backendPeer(t, execution, func(reader *bufio.Reader, peer net.Conn) {
				backendReply(peer, backendRead(t, reader, "initialize"), `{"protocolVersion":1,"agentCapabilities":{}}`)
				backendReply(peer, backendRead(t, reader, "session/new"), `{"sessionId":"task","configOptions":[{"id":"model","category":"model","type":"select","currentValue":"model","options":[{"value":"model"}]},{"id":"effort","category":"thought_level","type":"select","currentValue":"medium","options":[{"value":"medium"}]}]}`)
				backendReply(peer, backendRead(t, reader, "session/prompt"), `{"stopReason":"end_turn"}`)
				io.Copy(io.Discard, reader)
			})
			backend.ACP.Open = func(ctx context.Context, e Execution) (*acp.Connection, func() error, error) {
				argv := strings.Fields(provider.run)
				if e.Adapter.Name != provider.name || e.Invocation.Executable != filepath.Join(execution.Request.Cwd, argv[0]) || e.Invocation.Dir != execution.Request.Cwd || strings.Join(e.Invocation.Args, " ") != strings.Join(argv[1:], " ") {
					t.Errorf("launch identity: %+v", e)
				}
				return peerBackend.Open(ctx, e)
			}
			result, err := backend.Execute(context.Background(), execution)
			if err != nil || *calls != 0 || !result.Finished {
				t.Fatalf("result=%+v err=%v calls=%d", result, err, *calls)
			}
		})
	}
}
