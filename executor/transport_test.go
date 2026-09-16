package executor

import (
	"bufio"
	"context"
	"errors"
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
