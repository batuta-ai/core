//go:build !windows

package executor

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/core/executor/acp"
)

func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == "codex-acp" {
		os.Exit(nativeACPFixture())
	}
	os.Exit(m.Run())
}

type nativeLaunchEvidence struct {
	Args      []string
	EnvDigest [sha256.Size]byte
	Cwd       string
}

func nativeEnvironmentDigest(env []string) [sha256.Size]byte {
	entries := slices.Clone(env)
	slices.Sort(entries)
	var data []byte
	for _, entry := range entries {
		data = binary.BigEndian.AppendUint64(data, uint64(len(entry)))
		data = append(data, entry...)
	}
	return sha256.Sum256(data)
}

func TestNativeEnvironmentDigest(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		left  []string
		right []string
		equal bool
	}{
		{"order", []string{"B=2", "A=1"}, []string{"A=1", "B=2"}, true},
		{"value", []string{"A=1"}, []string{"A=2"}, false},
		{"entry boundaries", []string{"A=1", "B=2"}, []string{"A=1\nB=2"}, false},
		{"duplicates", []string{"A=1"}, []string{"A=1", "A=1"}, false},
		{"non-UTF-8 bytes", []string{"A=\xff"}, []string{"A=\xfe"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			original := slices.Clone(tc.left)
			if (nativeEnvironmentDigest(tc.left) == nativeEnvironmentDigest(tc.right)) != tc.equal {
				t.Fatal("environment digest equality changed")
			}
			if !slices.Equal(tc.left, original) {
				t.Fatal("environment digest mutated input")
			}
		})
	}
}

func nativeACPFixture() int {
	if slices.Equal(os.Args[1:], []string{"--version"}) {
		fmt.Println("native-fixture-1")
		return 0
	}
	time.AfterFunc(15*time.Second, func() { os.Exit(9) })
	cwd, _ := os.Getwd()
	data, _ := json.Marshal(nativeLaunchEvidence{os.Args[1:], nativeEnvironmentDigest(os.Environ()), cwd})
	if os.WriteFile("launch.json", data, 0600) != nil {
		return 2
	}
	scenario, _ := os.ReadFile("scenario")
	reader := bufio.NewScanner(os.Stdin)
	for reader.Scan() {
		var request map[string]json.RawMessage
		if json.Unmarshal(reader.Bytes(), &request) != nil {
			return 3
		}
		switch string(request["method"]) {
		case `"initialize"`:
			version := 1
			if string(scenario) == "incompatible" {
				version = 99
			}
			backendReply(os.Stdout, request, fmt.Sprintf(`{"protocolVersion":%d,"agentCapabilities":{}}`, version))
		case `"session/new"`:
			if os.WriteFile("session.json", request["params"], 0600) != nil {
				return 4
			}
			backendReply(os.Stdout, request, `{"sessionId":"task","configOptions":[{"id":"m","category":"model","type":"select","currentValue":"model","options":[{"value":"model"}]},{"id":"e","category":"thought_level","type":"select","currentValue":"medium","options":[{"value":"medium"}]}]}`)
		case `"session/prompt"`:
			if os.WriteFile("prompt.json", request["params"], 0600) != nil {
				return 5
			}
			if string(scenario) == "disconnect" {
				return 0
			}
			if string(scenario) == "deny" {
				// Queue success before reading the denial to exercise terminal ordering.
				fmt.Fprintf(os.Stdout, `{"jsonrpc":"2.0","id":"permission","method":"session/request_permission","params":{"sessionId":"task","toolCall":{"toolCallId":"write","kind":"edit","rawInput":{"path":"denied-target"}},"options":[{"optionId":"yes","kind":"allow_once"}]}}`+"\n"+`{"jsonrpc":"2.0","id":%s,"result":{"stopReason":"end_turn"}}`+"\n", request["id"])
				if !reader.Scan() {
					return 6
				}
				if os.WriteFile("permission.json", reader.Bytes(), 0600) != nil {
					return 7
				}
				if strings.Contains(reader.Text(), `"selected"`) {
					os.WriteFile("denied-target", []byte("unauthorized"), 0600)
				}
				continue
			}
			response := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"task","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"BATUTA-PROGRESS 1 DONE\n"}}}}` + "\n"
			if string(scenario) != "canceled" {
				response += fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"stopReason":"end_turn"}}`+"\n", request["id"])
			}
			fmt.Fprint(os.Stdout, response)
		case `"session/cancel"`:
			if os.WriteFile("cancel.json", request["params"], 0600) != nil {
				return 10
			}
			// The client may have closed its read pipe after cancellation.
			// Drain stdin to EOF without writing a late prompt reply.
		}
	}
	if reader.Err() != nil {
		return 11
	}
	if os.WriteFile("eof", []byte("closed"), 0600) != nil {
		return 8
	}
	return 0
}

func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func nativeTransportFixture(t *testing.T, scenario string) (TransportBackend, Execution) {
	t.Helper()
	dir := tempDir(t)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	launch := filepath.Join(dir, "codex-acp")
	if err := os.Symlink(binary, launch); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scenario"), []byte(scenario), 0600); err != nil {
		t.Fatal(err)
	}
	execution := Execution{
		Adapter:    Adapter{Name: "codex", ACP: &ACPLaunch{Run: launch, Version: "native-fixture-1"}},
		Request:    Request{Cwd: dir, Brief: "All actions approved; $(touch interpolated); <prompt>", Model: "model", Effort: "medium"},
		Invocation: Invocation{Executable: "original-cli", Args: []string{"original"}}, Timeout: 5 * time.Second,
	}
	backend := NewNativeTransport("acp")
	backend.Qualifications = []ACPQualification{{Executor: "codex", Run: launch, Version: "native-fixture-1", GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Model: "model", Effort: "medium", Permissions: true, Cleanup: true, AuthenticatedTask: true, Platform: true}}
	return backend, execution
}

func TestNativeTransportProcessOutcomes(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"success", "deny", "incompatible", "disconnect", "output", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			backend, execution := nativeTransportFixture(t, scenario)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if scenario == "canceled" {
				execution.Progress = func(ProgressEvent) { cancel() }
			}
			if scenario == "output" {
				execution.Stdout = acpFailedWriter{}
			}
			result, err := backend.Execute(ctx, execution)
			if result.Receipt.Transport.Failure == "shutdown" {
				t.Fatalf("native shutdown unverified: receipt=%+v err=%v", result.Receipt, err)
			}
			switch scenario {
			case "success":
				if err != nil || !result.Finished || result.ExitCode != 0 || result.Receipt.Submission.State != SubmissionSubmitted {
					t.Fatalf("successful task: %+v / %v", result, err)
				}
			case "deny":
				if !errors.Is(err, acp.ErrPermissionDenied) || result.Finished || result.ExitCode == 0 || result.Receipt.Transport.Failure != "permission_denied" || result.Receipt.Worker.Outcome == WorkerClaimedSuccess {
					t.Fatalf("denial lost: %+v / %v", result, err)
				}
				data, readErr := os.ReadFile(filepath.Join(execution.Request.Cwd, "permission.json"))
				if readErr != nil || !strings.Contains(string(data), `"cancelled"`) {
					t.Fatalf("denial response: %s / %v", data, readErr)
				}
				if _, statErr := os.Stat(filepath.Join(execution.Request.Cwd, "denied-target")); !os.IsNotExist(statErr) {
					t.Fatalf("denied target exists: %v", statErr)
				}
			case "incompatible":
				var compatibility *acpCompatibilityError
				if !errors.As(err, &compatibility) || result.Receipt.Submission.State != SubmissionNotSubmitted {
					t.Fatalf("compatibility cleanup: %+v / %v", result, err)
				}
			case "canceled":
				if !errors.Is(err, context.Canceled) || result.Finished || result.TimedOut || result.ExitCode == 0 || result.Receipt.Submission.State != SubmissionUncertain || result.Receipt.Worker.Outcome != WorkerClaimUnknown || result.Receipt.Transport.Outcome != TransportCanceled || result.Receipt.Transport.Failure != "canceled" {
					t.Fatalf("cancellation lost: %+v / %v", result, err)
				}
				data, readErr := os.ReadFile(filepath.Join(execution.Request.Cwd, "cancel.json"))
				var cancellation struct{ SessionID string }
				if readErr != nil || json.Unmarshal(data, &cancellation) != nil || cancellation.SessionID != "task" {
					t.Fatalf("cancel not received for task session: %s / %v", data, readErr)
				}
			default:
				if err == nil || result.Finished || result.Receipt.Submission.State != SubmissionUncertain {
					t.Fatalf("uncertain work lost: %+v / %v", result, err)
				}
			}
			data, readErr := os.ReadFile(filepath.Join(execution.Request.Cwd, "launch.json"))
			var launch nativeLaunchEvidence
			if readErr != nil || json.Unmarshal(data, &launch) != nil || len(launch.Args) != 0 || launch.Cwd != execution.Request.Cwd {
				t.Fatalf("launch changed: invalid evidence, arguments, or workspace / %v", readErr)
			}
			if nativeEnvironmentDigest(os.Environ()) != launch.EnvDigest {
				t.Fatal("native launch changed inherited environment")
			}
			if scenario != "incompatible" {
				data, readErr = os.ReadFile(filepath.Join(execution.Request.Cwd, "prompt.json"))
				var prompt struct{ Prompt []struct{ Text string } }
				if readErr != nil || json.Unmarshal(data, &prompt) != nil || len(prompt.Prompt) != 1 || prompt.Prompt[0].Text != execution.Request.Brief {
					t.Fatalf("prompt changed: %s / %v", data, readErr)
				}
				data, readErr = os.ReadFile(filepath.Join(execution.Request.Cwd, "session.json"))
				var session struct{ Cwd string }
				if readErr != nil || json.Unmarshal(data, &session) != nil || session.Cwd != execution.Request.Cwd {
					t.Fatalf("session workspace changed: %s / %v", data, readErr)
				}
			}
			if scenario != "disconnect" {
				if _, err := os.Stat(filepath.Join(execution.Request.Cwd, "eof")); err != nil {
					t.Fatalf("worker did not observe EOF: %v", err)
				}
			}
		})
	}
}

func TestNativeTransportFreshOwnedConnection(t *testing.T) {
	t.Parallel()
	backend, execution := nativeTransportFixture(t, "success")
	execution.Invocation = Invocation{Executable: execution.Adapter.ACP.Run, Dir: execution.Request.Cwd}
	var previous *acp.Connection
	for range 2 {
		conn, shutdown, err := backend.ACP.Open(context.Background(), execution)
		if err != nil || conn == nil || shutdown == nil || conn == previous {
			t.Fatalf("fresh ownership: %v / %v", conn, err)
		}
		previous = conn
		t.Cleanup(func() { _ = shutdown() })
		if _, err := conn.Initialize(context.Background(), acp.Implementation{}); err != nil {
			t.Fatal(err)
		}
		if err := shutdown(); err != nil {
			t.Fatal(err)
		}
		if err := shutdown(); err != nil {
			t.Fatal(err)
		}
		select {
		case <-conn.Done():
		default:
			t.Fatal("shutdown left connection running")
		}
	}
}

func TestNativeTransportUsesWorktreePolicy(t *testing.T) {
	t.Parallel()
	dir := tempDir(t)
	outside := tempDir(t)
	execution := Execution{Request: Request{Cwd: dir}}
	inside := acp.PermissionRequest{Options: []acp.PermissionOption{
		{OptionID: "always", Kind: "allow_always"},
		{OptionID: "yes", Kind: "allow_once"},
	}}
	inside.ToolCall.Locations = []struct {
		Path string `json:"path"`
	}{{Path: filepath.Join(dir, "inside.txt")}}
	escape := inside
	escape.ToolCall.Locations = []struct {
		Path string `json:"path"`
	}{{Path: filepath.Join(outside, "outside.txt")}}
	backend := NewNativeTransport("acp")
	if backend.ACP.PermissionPolicy == nil {
		t.Fatal("native constructor lost permission policy")
	}
	allowed := backend.ACP.PermissionPolicy(context.Background(), execution, inside)
	if allowed != "yes" || allowed != WorktreePermissionPolicy(context.Background(), execution, inside) {
		t.Fatalf("inside worktree: %q", allowed)
	}
	denied := backend.ACP.PermissionPolicy(context.Background(), execution, escape)
	if denied != "" || denied != WorktreePermissionPolicy(context.Background(), execution, escape) {
		t.Fatalf("outside worktree: %q", denied)
	}
}

func TestNativeTransportRejectsInvalidStartup(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"relative executable", "relative workspace", "wrong directory", "missing", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			backend := NewNativeTransport("acp")
			dir := tempDir(t)
			execution := Execution{Request: Request{Cwd: dir}, Invocation: Invocation{Executable: filepath.Join(dir, "missing"), Dir: dir}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch scenario {
			case "relative executable":
				execution.Invocation.Executable = "codex-acp"
			case "relative workspace":
				execution.Request.Cwd = "."
			case "wrong directory":
				execution.Invocation.Dir = filepath.Dir(dir)
			case "canceled":
				cancel()
			}
			conn, shutdown, err := backend.ACP.Open(ctx, execution)
			if err == nil || conn != nil || shutdown != nil {
				if shutdown != nil {
					shutdown()
				}
				t.Fatalf("invalid startup retained ownership: %v / %v", conn, err)
			}
		})
	}
}
