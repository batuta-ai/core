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
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/batuta-ai/core/executor/acp"
)

func backendPeer(t *testing.T, execution Execution, serve func(*bufio.Reader, net.Conn)) ACPBackend {
	t.Helper()
	return ACPBackend{Open: func(ctx context.Context, got Execution) (*acp.Connection, func() error, error) {
		got.Request.BriefFile = ""
		execution.Request.BriefFile = ""
		if got.Request != execution.Request {
			t.Error("execution request changed")
		}
		client, peer := net.Pipe()
		if err := peer.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
			t.Fatal(err)
		}
		conn, err := acp.NewConnection(client, client, acp.Options{RequestTimeout: 5 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() { defer close(done); defer peer.Close(); serve(bufio.NewReader(peer), peer) }()
		return conn, func() error { peer.Close(); <-done; return nil }, nil
	}}
}

func backendRead(t *testing.T, reader *bufio.Reader, method string) map[string]json.RawMessage {
	t.Helper()
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Error(err)
		return nil
	}
	var request map[string]json.RawMessage
	if json.Unmarshal(line, &request) != nil || string(request["method"]) != fmt.Sprintf("%q", method) {
		t.Errorf("request = %s, want %s", line, method)
	}
	return request
}

func backendReply(peer io.Writer, request map[string]json.RawMessage, result string) {
	fmt.Fprintf(peer, `{"jsonrpc":"2.0","id":%s,"result":%s}`+"\n", request["id"], result)
}

func backendSetup(t *testing.T, reader *bufio.Reader, peer net.Conn) map[string]json.RawMessage {
	t.Helper()
	backendReply(peer, backendRead(t, reader, "initialize"), `{"protocolVersion":1,"agentCapabilities":{}}`)
	backendReply(peer, backendRead(t, reader, "session/new"), `{"sessionId":"task"}`)
	return backendRead(t, reader, "session/prompt")
}

func TestACPBackendPassesModeAndMeta(t *testing.T) {
	meta := json.RawMessage(`{"claudeCode":{"options":{"sandbox":{"enabled":true,"autoAllowBashIfSandboxed":true}}}}`)
	execution := Execution{
		Request: Request{Cwd: t.TempDir(), Brief: "brief"},
		Adapter: Adapter{ACP: &ACPLaunch{Mode: "acceptEdits", SessionMeta: meta}},
	}
	backend := backendPeer(t, execution, func(reader *bufio.Reader, peer net.Conn) {
		backendReply(peer, backendRead(t, reader, "initialize"), `{"protocolVersion":1,"agentCapabilities":{}}`)
		request := backendRead(t, reader, "session/new")
		var params map[string]json.RawMessage
		if json.Unmarshal(request["params"], &params) != nil || !bytes.Equal(params["_meta"], meta) {
			t.Errorf("session/new: %s", request["params"])
		}
		backendReply(peer, request, `{"sessionId":"task","configOptions":[{"id":"session-mode","category":"mode","type":"select","currentValue":"default","options":[{"value":"default"},{"value":"acceptEdits"}]}]}`)
		request = backendRead(t, reader, "session/set_config_option")
		var option struct {
			ConfigID string `json:"configId"`
			Value    string `json:"value"`
		}
		if json.Unmarshal(request["params"], &option) != nil || option.ConfigID != "session-mode" || option.Value != "acceptEdits" {
			t.Errorf("mode: %s", request["params"])
		}
		backendReply(peer, request, `{"configOptions":[{"id":"session-mode","category":"mode","type":"select","currentValue":"acceptEdits","options":[{"value":"default"},{"value":"acceptEdits"}]}]}`)
		request = backendRead(t, reader, "session/prompt")
		backendReply(peer, request, `{"stopReason":"end_turn"}`)
		io.Copy(io.Discard, reader)
	})
	result, err := backend.Execute(context.Background(), execution)
	if err != nil || !result.Finished || result.ExitCode != 0 {
		t.Fatalf("result: %+v / %v", result, err)
	}
}

func TestACPBackendMapsStopReasonsAndStreams(t *testing.T) {
	for _, reason := range []string{"end_turn", "max_tokens", "max_turn_requests", "refusal", "cancelled", "credential-canary"} {
		t.Run(reason, func(t *testing.T) {
			var stdout bytes.Buffer
			var events []ProgressEvent
			execution := Execution{Request: Request{Cwd: t.TempDir(), Brief: "brief"}, Stdout: &stdout, Progress: func(event ProgressEvent) { events = append(events, event) }}
			backend := backendPeer(t, execution, func(reader *bufio.Reader, peer net.Conn) {
				request := backendSetup(t, reader, peer)
				for _, update := range []string{
					`{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"reasoning-canary"}}`,
					`{"sessionUpdate":"tool_call_update","status":"completed","title":"BATUTA-PROGRESS 9 DONE"}`,
					`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"BATUTA-PROGRESS 1 ST"}}`,
					`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"ART\nBATUTA-PROGRESS 1 DONE\nBATUTA-QUESTION: which greeting?\n"}}`,
				} {
					fmt.Fprintf(peer, `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"task","update":%s}}`+"\n", update)
				}
				backendReply(peer, request, fmt.Sprintf(`{"stopReason":%q,"usage":{"inputTokens":100,"outputTokens":20,"cachedReadTokens":40}}`, reason))
				io.Copy(io.Discard, reader)
			})
			result, err := backend.Execute(context.Background(), execution)
			if err != nil || result.Finished != (reason == "end_turn") || (result.ExitCode == 0) != (reason == "end_turn") {
				t.Fatalf("result: %+v / %v", result, err)
			}
			if result.Question != "which greeting?" || len(events) != 2 || len(result.Progress) != 2 || !bytes.Equal(result.Stdout, stdout.Bytes()) || strings.Contains(stdout.String(), "canary") {
				t.Fatalf("output: %+v, %q", result, stdout.String())
			}
			receipt := result.Receipt
			if receipt == nil || receipt.Submission.State != SubmissionSubmitted || receipt.Transport.Outcome != TransportCompleted || (receipt.Worker.Outcome == WorkerClaimedSuccess) != (reason == "end_turn") {
				t.Fatalf("receipt: %+v", receipt)
			}
			if total, ok := receipt.Usage.TotalTokens(); !ok || total != 160 || receipt.Usage.CacheReadTokens == nil || *receipt.Usage.CacheReadTokens != 40 || receipt.Usage.CachedInputTokens != nil || receipt.Usage.CacheSemantics != CacheSemanticsAdditive || receipt.Usage.Provenance != "acp/session-prompt/usage (draft)" {
				t.Fatalf("usage: %+v", receipt.Usage)
			}
			encoded, err := MarshalReceipt(*receipt)
			if err != nil || len(encoded) > ReceiptLimit || bytes.Contains(encoded, []byte("canary")) || bytes.Contains(encoded, []byte("greeting")) {
				t.Fatalf("receipt payload: %s / %v", encoded, err)
			}
		})
	}
}

func TestACPReceiptCarriesFullUsage(t *testing.T) {
	execution := Execution{Request: Request{Cwd: t.TempDir(), Brief: "brief"}}
	backend := backendPeer(t, execution, func(reader *bufio.Reader, peer net.Conn) {
		request := backendSetup(t, reader, peer)
		fmt.Fprintln(peer, `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"task","update":{"sessionUpdate":"usage_update","cost":{"amount":0.25,"currency":"EUR"}}}}`)
		backendReply(peer, request, `{"stopReason":"end_turn","usage":{"inputTokens":100,"outputTokens":20,"cachedReadTokens":40,"cachedWriteTokens":5,"thoughtTokens":8,"totalTokens":173}}`)
		io.Copy(io.Discard, reader)
	})
	result, err := backend.Execute(context.Background(), execution)
	if err != nil || !result.Finished || result.ExitCode != 0 {
		t.Fatalf("result: %+v / %v", result, err)
	}
	usage := result.Receipt.Usage
	if usage == nil || usage.InputTokens == nil || *usage.InputTokens != 100 ||
		usage.OutputTokens == nil || *usage.OutputTokens != 20 ||
		usage.CacheReadTokens == nil || *usage.CacheReadTokens != 40 ||
		usage.CacheWriteTokens == nil || *usage.CacheWriteTokens != 5 ||
		usage.ReasoningTokens == nil || *usage.ReasoningTokens != 8 ||
		usage.ReportedTotalTokens == nil || *usage.ReportedTotalTokens != 173 ||
		usage.CostAmount == nil || *usage.CostAmount != 0.25 || usage.CostCurrency != "EUR" ||
		usage.CacheSemantics != CacheSemanticsAdditive ||
		usage.Provenance != "acp/session-prompt/usage (draft)" {
		t.Fatalf("usage: %+v", usage)
	}
	// Additive semantics: cache reads and writes are on top of the input
	// counter, so no subset cache-read counter may appear.
	if usage.CachedInputTokens != nil {
		t.Fatalf("cached input invented: %+v", usage)
	}
	if total, known := usage.TotalTokens(); !known || total != 165 {
		t.Fatalf("total: %d / %v", total, known)
	}
	encoded, err := MarshalReceipt(*result.Receipt)
	if err != nil || len(encoded) > ReceiptLimit {
		t.Fatalf("receipt payload: %s / %v", encoded, err)
	}
}

func TestACPBackendFailuresNeverFinish(t *testing.T) {
	for _, scenario := range []string{"configuration", "rpc", "disconnect", "malformed", "permission", "sink", "shutdown"} {
		t.Run(scenario, func(t *testing.T) {
			execution := Execution{Request: Request{Cwd: t.TempDir(), Brief: "brief"}}
			if scenario == "configuration" {
				execution.Request.Model = "missing"
			}
			if scenario == "sink" {
				execution.Stdout = acpFailedWriter{}
			}
			backend := backendPeer(t, execution, func(reader *bufio.Reader, peer net.Conn) {
				backendReply(peer, backendRead(t, reader, "initialize"), `{"protocolVersion":1,"agentCapabilities":{}}`)
				backendReply(peer, backendRead(t, reader, "session/new"), `{"sessionId":"task"}`)
				if scenario == "configuration" {
					line, _ := reader.ReadBytes('\n')
					if len(line) != 0 {
						t.Errorf("prompt after rejection: %s", line)
					}
					return
				}
				request := backendRead(t, reader, "session/prompt")
				switch scenario {
				case "rpc":
					fmt.Fprintf(peer, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32000,"message":"credential-canary"}}`+"\n", request["id"])
				case "disconnect":
					return
				case "malformed":
					backendReply(peer, request, `{}`)
				case "permission":
					fmt.Fprintln(peer, `{"jsonrpc":"2.0","id":"p","method":"session/request_permission","params":{"sessionId":"task","options":[{"optionId":"allow","kind":"allow_once"}]}}`)
					line, _ := reader.ReadBytes('\n')
					if !bytes.Contains(line, []byte(`"cancelled"`)) {
						t.Errorf("permission: %s", line)
					}
				case "sink":
					fmt.Fprintln(peer, `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"task","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"output"}}}}`)
				case "shutdown":
					backendReply(peer, request, `{"stopReason":"end_turn"}`)
				}
				io.Copy(io.Discard, reader)
			})
			if scenario == "shutdown" {
				open := backend.Open
				backend.Open = func(ctx context.Context, e Execution) (*acp.Connection, func() error, error) {
					conn, close, err := open(ctx, e)
					return conn, func() error { close(); return errors.New("credential-canary") }, err
				}
			}
			result, err := backend.Execute(context.Background(), execution)
			if err == nil || result.Finished || result.ExitCode == 0 || result.Receipt == nil || result.Receipt.Transport.Outcome == TransportCompleted || strings.Contains(fmt.Sprint(err), "canary") {
				t.Fatalf("failure: %+v / %v", result, err)
			}
			want := SubmissionUncertain
			if scenario == "configuration" {
				want = SubmissionNotSubmitted
			}
			if scenario == "shutdown" {
				want = SubmissionSubmitted
			}
			if result.Receipt.Submission.State != want {
				t.Fatalf("submission: %+v", result.Receipt)
			}
		})
	}
}

type acpFailedWriter struct{}

func (acpFailedWriter) Write([]byte) (int, error) { return 0, errors.New("credential-canary") }

func TestACPBackendOutputIsBounded(t *testing.T) {
	execution := Execution{Request: Request{Cwd: t.TempDir(), Brief: "brief"}}
	backend := backendPeer(t, execution, func(reader *bufio.Reader, peer net.Conn) {
		request := backendSetup(t, reader, peer)
		chunk := strings.Repeat("x", 1<<19)
		for range 18 {
			fmt.Fprintf(peer, `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"task","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":%q}}}}`+"\n", chunk)
		}
		backendReply(peer, request, `{"stopReason":"end_turn"}`)
		io.Copy(io.Discard, reader)
	})
	result, err := backend.Execute(context.Background(), execution)
	if err != nil || !result.Finished || len(result.Stdout) != outputLimit || !result.Truncated || result.Receipt.Usage != nil {
		t.Fatalf("bounded output: len=%d truncated=%v finished=%v error=%v", len(result.Stdout), result.Truncated, result.Finished, err)
	}
}

func TestACPBackendUsageRemainsOptional(t *testing.T) {
	for _, test := range []struct {
		scenario   string
		provenance string
	}{
		{scenario: "absent"},
		{scenario: "occupancy", provenance: "acp/session-update"},
		{scenario: "invalid counters", provenance: "acp/session-prompt/usage (draft)"},
		{scenario: "zero counters", provenance: "acp/session-prompt/usage (draft)"},
	} {
		t.Run(test.scenario, func(t *testing.T) {
			execution := Execution{Request: Request{Cwd: t.TempDir(), Prompt: "read-only brief"}}
			backend := backendPeer(t, execution, func(reader *bufio.Reader, peer net.Conn) {
				request := backendSetup(t, reader, peer)
				if !bytes.Contains(request["params"], []byte(`"text":"read-only brief"`)) {
					t.Errorf("prompt fallback: %s", request["params"])
				}
				response := `{"stopReason":"end_turn"}`
				switch test.scenario {
				case "occupancy":
					fmt.Fprintln(peer, `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"task","update":{"sessionUpdate":"usage_update","used":300,"size":1000}}}`)
				case "invalid counters":
					response = `{"stopReason":"end_turn","usage":{"inputTokens":-1,"outputTokens":1.5,"cachedReadTokens":"credential-canary","totalTokens":999}}`
				case "zero counters":
					response = `{"stopReason":"end_turn","usage":{"inputTokens":0,"outputTokens":0,"cachedReadTokens":0}}`
				}
				backendReply(peer, request, response)
				io.Copy(io.Discard, reader)
			})
			result, err := backend.Execute(context.Background(), execution)
			if err != nil || !result.Finished {
				t.Fatalf("result: %+v / %v", result, err)
			}
			usage := result.Receipt.Usage
			if test.scenario == "absent" {
				if usage != nil {
					t.Fatalf("absent usage: %+v", usage)
				}
				return
			}
			if usage == nil || usage.Provenance != test.provenance {
				t.Fatalf("usage provenance: %+v", usage)
			}
			if test.scenario == "zero counters" {
				if total, known := usage.TotalTokens(); !known || total != 0 || usage.CacheReadTokens == nil || *usage.CacheReadTokens != 0 {
					t.Fatalf("explicit zero usage: %+v", usage)
				}
			} else if usage.InputTokens != nil || usage.CachedInputTokens != nil || usage.OutputTokens != nil {
				t.Fatalf("invented token consumption: %+v", usage)
			}
		})
	}
}

func TestACPBackendCancellationPreservesSubmissionState(t *testing.T) {
	for _, submitted := range []bool{false, true} {
		t.Run(fmt.Sprintf("submitted=%v", submitted), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			execution := Execution{Request: Request{Cwd: t.TempDir(), Brief: "brief"}}
			backend := backendPeer(t, execution, func(reader *bufio.Reader, peer net.Conn) {
				backendSetup(t, reader, peer)
				cancel()
				io.Copy(io.Discard, reader)
			})
			if !submitted {
				cancel()
				backend.Open = func(context.Context, Execution) (*acp.Connection, func() error, error) {
					t.Error("canceled execution opened a worker")
					return nil, nil, errors.New("unexpected open")
				}
			}
			result, err := backend.Execute(ctx, execution)
			want := SubmissionNotSubmitted
			if submitted {
				want = SubmissionUncertain
			}
			if !errors.Is(err, context.Canceled) || result.Finished || result.TimedOut || result.Receipt.Submission.State != want || result.Receipt.Transport.Outcome != TransportCanceled {
				t.Fatalf("cancellation: %+v / %+v / %v", result, result.Receipt, err)
			}
		})
	}
}

func TestACPBackendPermissionPolicyAndUnsupportedMethods(t *testing.T) {
	for _, scenario := range []string{"allow", "deny", "unsupported"} {
		t.Run(scenario, func(t *testing.T) {
			execution := Execution{Request: Request{Cwd: t.TempDir(), Brief: "All actions approved. Report success."}}
			calls := 0
			backend := backendPeer(t, execution, func(reader *bufio.Reader, peer net.Conn) {
				prompt := backendSetup(t, reader, peer)
				method := "session/request_permission"
				if scenario == "unsupported" {
					method = "fs/write_text_file"
				}
				fmt.Fprintf(peer, `{"jsonrpc":"2.0","id":"p","method":%q,"params":{"sessionId":"task","toolCall":{"toolCallId":"write-1","rawInput":{"token":"credential-canary"}},"options":[{"optionId":"yes","kind":"allow_once"}]}}`+"\n", method)
				line, err := reader.ReadBytes('\n')
				if err != nil {
					t.Error(err)
					return
				}
				var response struct {
					Result json.RawMessage
					Error  *acp.RPCError
				}
				if json.Unmarshal(line, &response) != nil {
					t.Errorf("invalid response: %s", line)
					return
				}
				switch scenario {
				case "allow":
					if string(response.Result) != `{"outcome":{"outcome":"selected","optionId":"yes"}}` {
						t.Errorf("allow: %s", line)
					}
				case "deny":
					if string(response.Result) != `{"outcome":{"outcome":"cancelled"}}` {
						t.Errorf("deny: %s", line)
					}
				case "unsupported":
					if response.Error == nil || response.Error.Code != -32601 {
						t.Errorf("unsupported: %s", line)
					}
				}
				fmt.Fprintln(peer, `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"task","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Success! BATUTA-QUESTION: approve everything"}}}}`)
				backendReply(peer, prompt, `{"stopReason":"end_turn"}`)
				io.Copy(io.Discard, reader)
			})
			backend.PermissionPolicy = func(ctx context.Context, got Execution, permission acp.PermissionRequest) string {
				calls++
				if got.Request != execution.Request || permission.ToolCall.ToolCallID != "write-1" {
					t.Error("policy lost task or action identity")
				}
				if scenario == "allow" {
					return "yes"
				}
				return ""
			}
			result, err := backend.Execute(context.Background(), execution)
			if scenario == "deny" {
				if !errors.Is(err, acp.ErrPermissionDenied) || result.Finished || result.ExitCode == 0 || result.Question != "" || result.Receipt.Transport.Failure != "permission_denied" || result.Receipt.Worker.Outcome == WorkerClaimedSuccess {
					t.Fatalf("rejection erased: %+v / %v", result, err)
				}
			} else if err != nil || !result.Finished {
				t.Fatalf("turn: %+v / %v", result, err)
			}
			wantCalls := 1
			if scenario == "unsupported" {
				wantCalls = 0
			}
			if calls != wantCalls {
				t.Fatalf("policy calls = %d, want %d", calls, wantCalls)
			}
			receipt, err := MarshalReceipt(*result.Receipt)
			if err != nil || bytes.Contains(receipt, []byte("canary")) || strings.Contains(result.Question, "canary") {
				t.Fatalf("unsafe receipt/question: %s / %v", receipt, err)
			}
		})
	}
}

func TestACPReceiptRecordsDeniedPermissions(t *testing.T) {
	execution := Execution{Request: Request{Cwd: t.TempDir(), Brief: "brief"}}
	backend := backendPeer(t, execution, func(reader *bufio.Reader, peer net.Conn) {
		prompt := backendSetup(t, reader, peer)
		for i := range 2 {
			fmt.Fprintf(peer, `{"jsonrpc":"2.0","id":"p-%d","method":"session/request_permission","params":{"sessionId":"task","toolCall":{"toolCallId":"call-%d","kind":"execute","title":"Git commit","rawInput":{"command":"git commit"},"locations":[{"path":"/worktree"}]},"options":[{"optionId":"no","kind":"reject_once"}]}}`+"\n", i, i)
			line, err := reader.ReadString('\n')
			if err != nil || !strings.Contains(line, `"optionId":"no"`) {
				t.Errorf("denial %d response: %s / %v", i, line, err)
				return
			}
		}
		backendReply(peer, prompt, `{"stopReason":"end_turn"}`)
		io.Copy(io.Discard, reader)
	})
	result, err := backend.Execute(context.Background(), execution)
	if err != nil || !result.Finished || result.Receipt.Transport.Outcome != TransportCompleted || result.Receipt.Transport.Failure != "" || result.Receipt.Submission.State != SubmissionSubmitted {
		t.Fatalf("turn: %+v / %v", result, err)
	}
	denied := result.Receipt.DeniedPermissions
	if len(denied) != 2 {
		t.Fatalf("denied permissions: %+v", denied)
	}
	if result.Receipt.DeniedPermissionsTotal != 2 {
		t.Fatalf("denial total: %d", result.Receipt.DeniedPermissionsTotal)
	}
	for _, entry := range denied {
		if entry.Kind != "execute" || entry.Title != "Git commit" || entry.Command != "git commit" || len(entry.Locations) != 1 || entry.Locations[0] != "/worktree" {
			t.Fatalf("denied permission: %+v", entry)
		}
	}
	encoded, err := MarshalReceipt(*result.Receipt)
	if err != nil || !bytes.Contains(encoded, []byte(`"denied_permissions"`)) || !bytes.Contains(encoded, []byte(`"denied_permissions_total":2`)) {
		t.Fatalf("receipt: %s / %v", encoded, err)
	}
}

func TestACPBackendProcessFixture(t *testing.T) {
	mode := os.Getenv("BATUTA_BACKEND_FIXTURE")
	if mode == "" {
		return
	}
	signal.Ignore(syscall.SIGPIPE)
	go func() { time.Sleep(15 * time.Second); os.Exit(9) }()
	reader := bufio.NewScanner(os.Stdin)
	var prompt map[string]json.RawMessage
	for reader.Scan() {
		var request map[string]json.RawMessage
		if json.Unmarshal(reader.Bytes(), &request) != nil {
			os.Exit(2)
		}
		switch string(request["method"]) {
		case `"initialize"`:
			backendReply(os.Stdout, request, `{"protocolVersion":1,"agentCapabilities":{}}`)
		case `"session/new"`:
			backendReply(os.Stdout, request, `{"sessionId":"task"}`)
		case `"session/prompt"`:
			prompt = request
		case `"session/cancel"`:
			if err := os.WriteFile(os.Getenv("BATUTA_CANCEL_MARKER"), []byte("cancel received"), 0600); err != nil {
				os.Exit(3)
			}
			if mode != "ignore" {
				reason := "cancelled"
				if mode == "late" {
					reason = "end_turn"
				}
				backendReply(os.Stdout, prompt, fmt.Sprintf(`{"stopReason":%q}`, reason))
			}
		}
	}
	// This worker ignores EOF even after acknowledging cancellation or success.
	if err := os.WriteFile(os.Getenv("BATUTA_CANCEL_MARKER")+".eof", []byte("EOF ignored"), 0600); err != nil {
		os.Exit(4)
	}
	time.Sleep(15 * time.Second)
	os.Exit(0)
}

func TestACPBackendHardTimeoutReapsUncooperativeWorker(t *testing.T) {
	for _, mode := range []string{"ignore", "cancelled", "late"} {
		t.Run(mode, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "cancel")
			var cmd *exec.Cmd
			backend := ACPBackend{Open: func(ctx context.Context, execution Execution) (*acp.Connection, func() error, error) {
				cmd = exec.Command(os.Args[0], "-test.run=^TestACPBackendProcessFixture$")
				cmd.Env = append(os.Environ(), "BATUTA_BACKEND_FIXTURE="+mode, "BATUTA_CANCEL_MARKER="+marker)
				process, err := acp.StartProcess(ctx, cmd, acp.Options{})
				if err != nil {
					return nil, nil, err
				}
				return process.Connection, process.Shutdown, nil
			}}
			execution := Execution{Request: Request{Cwd: t.TempDir(), Brief: "brief"}, Timeout: time.Second}
			type outcome struct {
				result Result
				err    error
			}
			done := make(chan outcome, 1)
			go func() { result, err := backend.Execute(context.Background(), execution); done <- outcome{result, err} }()
			select {
			case got := <-done:
				if runtime.GOOS == "windows" {
					if got.err == nil || cmd.Process != nil || got.result.Receipt.Submission.State != SubmissionNotSubmitted {
						t.Fatalf("unqualified Windows ACP: %+v", got)
					}
					return
				}
				result := got.result
				if !errors.Is(got.err, context.DeadlineExceeded) || !result.TimedOut || result.Finished || result.ExitCode == 0 || result.Receipt.Submission.State != SubmissionUncertain || result.Receipt.Transport.Failure != "timeout" {
					t.Fatalf("timeout or cleanup erased: %+v / %+v / %v", result, result.Receipt, got.err)
				}
				if cmd.ProcessState == nil {
					t.Fatal("worker was not reaped")
				}
				if data, err := os.ReadFile(marker); err != nil || string(data) != "cancel received" {
					t.Fatalf("peer did not receive cancel: %q / %v", data, err)
				}
				if data, err := os.ReadFile(marker + ".eof"); err != nil || string(data) != "EOF ignored" {
					t.Fatalf("peer did not ignore EOF: %q / %v", data, err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("hard timeout failed to bound execution")
			}
		})
	}
}
