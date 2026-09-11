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
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/core/executor/acp"
)

func backendPeer(t *testing.T, execution Execution, serve func(*bufio.Reader, net.Conn)) ACPBackend {
	t.Helper()
	return ACPBackend{Open: func(ctx context.Context, got Execution) (*acp.Connection, func() error, error) {
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
			if total, ok := receipt.Usage.TotalTokens(); !ok || total != 120 || *receipt.Usage.CachedInputTokens != 40 {
				t.Fatalf("usage: %+v", receipt.Usage)
			}
			encoded, err := MarshalReceipt(*receipt)
			if err != nil || len(encoded) > ReceiptLimit || bytes.Contains(encoded, []byte("canary")) || bytes.Contains(encoded, []byte("greeting")) {
				t.Fatalf("receipt payload: %s / %v", encoded, err)
			}
		})
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
	for _, scenario := range []string{"absent", "occupancy", "invalid counters", "zero counters"} {
		t.Run(scenario, func(t *testing.T) {
			execution := Execution{Request: Request{Cwd: t.TempDir(), Prompt: "read-only brief"}}
			backend := backendPeer(t, execution, func(reader *bufio.Reader, peer net.Conn) {
				request := backendSetup(t, reader, peer)
				if !bytes.Contains(request["params"], []byte(`"text":"read-only brief"`)) {
					t.Errorf("prompt fallback: %s", request["params"])
				}
				response := `{"stopReason":"end_turn"}`
				switch scenario {
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
			if scenario == "absent" {
				if usage != nil {
					t.Fatalf("absent usage: %+v", usage)
				}
				return
			}
			if usage == nil || usage.Provenance == "" {
				t.Fatalf("usage provenance: %+v", usage)
			}
			if scenario == "zero counters" {
				if total, known := usage.TotalTokens(); !known || total != 0 || usage.CachedInputTokens == nil || *usage.CachedInputTokens != 0 {
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
