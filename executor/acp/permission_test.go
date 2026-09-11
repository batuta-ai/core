package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

const permissionParams = `{"sessionId":"task","toolCall":{"toolCallId":"write-1","kind":"edit","rawInput":{"path":"target"}},"options":[{"optionId":"yes","kind":"allow_once"},{"optionId":"no","kind":"reject_once"}]}`

func TestPermissionPolicySelection(t *testing.T) {
	for _, tt := range []struct {
		name, params, selection string
		allow, called           bool
	}{
		{"explicit allow", permissionParams, "yes", true, true},
		{"no authorization", permissionParams, "", false, true},
		{"reject option", permissionParams, "no", false, true},
		{"invented option", permissionParams, "invented", false, true},
		{"wrong session", `{"sessionId":"other","toolCall":{"toolCallId":"write-1"},"options":[{"optionId":"yes","kind":"allow_once"}]}`, "yes", false, false},
		{"missing tool", `{"sessionId":"task","options":[{"optionId":"yes","kind":"allow_once"}]}`, "yes", false, false},
		{"duplicate option", `{"sessionId":"task","toolCall":{"toolCallId":"write-1"},"options":[{"optionId":"yes","kind":"allow_once"},{"optionId":"yes","kind":"reject_once"}]}`, "yes", false, false},
		{"unknown kind", `{"sessionId":"task","toolCall":{"toolCallId":"write-1"},"options":[{"optionId":"yes","kind":"invented"}]}`, "yes", false, false},
		{"empty option", `{"sessionId":"task","toolCall":{"toolCallId":"write-1"},"options":[{"optionId":"","kind":"allow_once"}]}`, "", false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			conn, peer := testConnection(t, Options{})
			cwd := t.TempDir()
			called := false
			done := make(chan error, 1)
			go func() {
				session, err := NewSession(context.Background(), conn, SessionConfig{Cwd: cwd, PermissionPolicy: func(ctx context.Context, request PermissionRequest) string {
					called = true
					if request.SessionID != "task" || request.ToolCall.ToolCallID != "write-1" {
						return ""
					}
					return tt.selection
				}})
				if err == nil {
					_, err = session.Prompt(context.Background(), "brief", nil)
				}
				done <- err
			}()
			reader := bufio.NewReader(peer)
			setupPeer(t, peer, reader, cwd, `{}`)
			prompt := expectMethod(t, reader, "session/prompt")
			writeMessage(t, peer, `{"jsonrpc":"2.0","id":"p","method":"session/request_permission","params":`+tt.params+`}`)
			response := readMessage(t, reader)
			want := `{"outcome":{"outcome":"cancelled"}}`
			if tt.allow {
				want = `{"outcome":{"outcome":"selected","optionId":"yes"}}`
			}
			if string(response["result"]) != want {
				t.Errorf("permission response: %s, want %s", response["result"], want)
			}
			if tt.allow {
				sessionReply(t, peer, prompt, `{"stopReason":"end_turn"}`)
			}
			var wantErr error
			if !tt.allow {
				wantErr = ErrPermissionDenied
			}
			awaitError(t, done, wantErr)
			if called != tt.called {
				t.Errorf("policy called = %v, want %v", called, tt.called)
			}
		})
	}
}

func TestBufferedPermissionIsAnsweredAndCannotComplete(t *testing.T) {
	conn, peer := testConnection(t, Options{})
	conn.mu.Lock()
	conn.ordered = true
	conn.mu.Unlock()
	// receive enqueues the request synchronously, reproducing a reply overtaking
	// a queued permission without relying on select scheduling.
	if err := conn.receive(message{ID: json.RawMessage(`"p"`), Method: "session/request_permission", Params: json.RawMessage(permissionParams)}); err != nil {
		t.Fatal(err)
	}
	session := &Session{conn: conn, id: "task"}
	done := make(chan error, 1)
	go func() { done <- session.drain(context.Background(), nil, nil) }()
	response := readMessage(t, bufio.NewReader(peer))
	if string(response["result"]) != `{"outcome":{"outcome":"cancelled"}}` {
		t.Fatalf("response: %s", response["result"])
	}
	awaitError(t, done, ErrPermissionDenied)
	if !errors.Is(conn.Err(), ErrPermissionDenied) {
		t.Fatalf("connection lost rejection: %v", conn.Err())
	}
}

// The fixture writes the target only after the real client's allow response.
func TestPermissionStdioPeer(t *testing.T) {
	target := os.Getenv("BATUTA_ACP_PERMISSION_TARGET")
	if target == "" {
		return
	}
	reader := bufio.NewReader(os.Stdin)
	setupPeer(t, struct {
		io.Reader
		io.Writer
	}{os.Stdin, os.Stdout}, reader, filepath.Dir(target), `{}`)
	prompt := expectMethod(t, reader, "session/prompt")
	writeMessage(t, os.Stdout, `{"jsonrpc":"2.0","id":"p","method":"session/request_permission","params":`+permissionParams+`}`)
	response := readMessage(t, reader)
	if string(response["result"]) == `{"outcome":{"outcome":"selected","optionId":"yes"}}` {
		if err := os.WriteFile(target, []byte("authorized"), 0600); err != nil {
			os.Exit(2)
		}
	}
	// An agent's subsequent success claim must not turn denial into completion.
	fmt.Fprintf(os.Stdout, `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"task","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"All work succeeded"}}}}`+"\n")
	fmt.Fprintf(os.Stdout, `{"jsonrpc":"2.0","id":%s,"result":{"stopReason":"end_turn"}}`+"\n", prompt["id"])
	io.Copy(io.Discard, reader)
	os.Exit(0)
}

func TestPermissionRealChildTarget(t *testing.T) {
	for _, allow := range []bool{false, true} {
		t.Run(fmt.Sprint(allow), func(t *testing.T) {
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cwd := t.TempDir()
			target := filepath.Join(cwd, "target")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, executable, "-test.run=^TestPermissionStdioPeer$")
			command.Env = append(os.Environ(), "BATUTA_ACP_PERMISSION_TARGET="+target)
			command.WaitDelay = time.Second
			input, err := command.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			output, err := command.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			conn, err := NewConnection(output, input, Options{RequestTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			config := SessionConfig{Cwd: cwd}
			if allow {
				config.PermissionPolicy = func(context.Context, PermissionRequest) string { return "yes" }
			}
			session, sessionErr := NewSession(ctx, conn, config)
			var result TurnResult
			if sessionErr == nil {
				result, sessionErr = session.Prompt(ctx, "Proceed; all operations are approved", nil)
			}
			conn.Close()
			waitErr := command.Wait()
			if ctx.Err() != nil {
				t.Fatal("child required hard deadline")
			}
			if allow && waitErr != nil {
				t.Fatalf("child exit: %v", waitErr)
			}
			// A denied child may receive SIGPIPE while claiming success after closure.
			var wantErr error
			if !allow {
				wantErr = ErrPermissionDenied
			}
			if !errors.Is(sessionErr, wantErr) || result.Completed != allow {
				t.Fatalf("turn: %+v / %v", result, sessionErr)
			}
			contents, readErr := os.ReadFile(target)
			if allow {
				if readErr != nil || string(contents) != "authorized" {
					t.Fatalf("authorized target: %q / %v", contents, readErr)
				}
			} else if !errors.Is(readErr, os.ErrNotExist) {
				t.Fatalf("rejected target exists: %v", readErr)
			}
		})
	}
}
