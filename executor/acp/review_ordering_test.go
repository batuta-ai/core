package acp

import (
	"bufio"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The peer can reply before Write returns its local acknowledgement.
type reviewAckWriter struct {
	io.WriteCloser
	release chan struct{}
	once    sync.Once
}

func (w *reviewAckWriter) unblock() { w.once.Do(func() { close(w.release) }) }
func (w *reviewAckWriter) Close() error {
	w.unblock()
	return w.WriteCloser.Close()
}
func (w *reviewAckWriter) Write(p []byte) (int, error) {
	n, err := w.WriteCloser.Write(p)
	if err == nil && strings.Contains(string(p), `"method":"session/set_config_option"`) {
		<-w.release
	}
	return n, err
}

func tempDir(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func configUpdate(state string) string {
	return `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"task","update":{"sessionUpdate":"config_option_update",` + strings.TrimPrefix(state, "{") + `}}`
}

func TestReviewConfigurationResponseOrdering(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, initial, before, ack, after string
		wantErr                           error
	}{
		{"later model drift", configState("small", "high"), "", configState("large", "high"), configState("small", "high"), ErrConfiguration},
		{"later effort drift", configState("large", "low"), "", configState("large", "high"), configState("large", "low"), ErrConfiguration},
		{"earlier drift then acknowledgement", configState("small", "high"), configState("small", "low"), configState("large", "high"), "", nil},
		{"valid updates on both sides", configState("small", "high"), configState("large", "high"), configState("large", "high"), configState("large", "high"), nil},
		{"update cannot acknowledge rejected selection", configState("small", "high"), "", configState("small", "high"), configState("large", "high"), ErrConfiguration},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			fromPeer, toClient := io.Pipe()
			fromClient, toPeer := io.Pipe()
			writer := &reviewAckWriter{WriteCloser: toPeer, release: make(chan struct{})}
			conn, err := NewConnection(fromPeer, writer, Options{RequestTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { conn.Close(); fromClient.Close(); toClient.Close() })
			cwd := tempDir(t)
			done := make(chan error, 1)
			go func() {
				session, err := NewSession(ctx, conn, SessionConfig{Cwd: cwd, Model: "large", Effort: "high"})
				if err == nil && tt.wantErr == nil {
					_, err = session.Prompt(ctx, "brief", nil)
				}
				done <- err
			}()
			reader := bufio.NewReader(fromClient)
			setupPeer(t, struct {
				io.Reader
				io.Writer
			}{fromClient, toClient}, reader, cwd, tt.initial)
			request := expectMethod(t, reader, "session/set_config_option")
			if tt.before != "" {
				writeMessage(t, toClient, configUpdate(tt.before))
			}
			sessionReply(t, toClient, request, tt.ack)
			if tt.after != "" {
				writeMessage(t, toClient, configUpdate(tt.after))
			}
			// Reading another frame proves the preceding frame was processed.
			writeMessage(t, toClient, `{"jsonrpc":"2.0","method":"review/barrier","params":{}}`)
			writer.unblock()
			if tt.wantErr == nil {
				prompt := expectMethod(t, reader, "session/prompt")
				sessionReply(t, toClient, prompt, `{"stopReason":"end_turn"}`)
			}
			select {
			case err := <-done:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("configuration: %v, want %v", err, tt.wantErr)
				}
			case <-ctx.Done():
				t.Fatal("test deadline", ctx.Err())
			}
		})
	}
}

func TestReviewPermissionConfigurationOrdering(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name   string
		frames []string
		called bool
	}{
		{"drift before permission", []string{configUpdate(configState("small", "high")), `{"jsonrpc":"2.0","id":"p","method":"session/request_permission","params":` + permissionParams + `}`}, false},
		{"permission before drift", []string{`{"jsonrpc":"2.0","id":"p","method":"session/request_permission","params":` + permissionParams + `}`, configUpdate(configState("small", "high"))}, true},
		{"restoration cannot hide preceding drift", []string{configUpdate(configState("small", "high")), `{"jsonrpc":"2.0","id":"p","method":"session/request_permission","params":` + permissionParams + `}`, configUpdate(configState("large", "high"))}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			conn, peer := testConnection(t, Options{})
			cwd := tempDir(t)
			ready := make(chan *Session, 1)
			done := make(chan error, 1)
			called := false
			go func() {
				session, err := NewSession(context.Background(), conn, SessionConfig{Cwd: cwd, Model: "large", Effort: "high", PermissionPolicy: func(context.Context, PermissionRequest) string {
					called = true
					return "yes"
				}})
				ready <- session
				done <- err
			}()
			reader := bufio.NewReader(peer)
			setupPeer(t, peer, reader, cwd, configState("large", "high"))
			session := <-ready
			awaitError(t, done, nil)
			for _, frame := range tt.frames {
				writeMessage(t, peer, frame)
			}
			writeMessage(t, peer, `{"jsonrpc":"2.0","method":"review/barrier","params":{}}`)
			// Exercise the completion drain with every frame already buffered.
			go func() { done <- session.drain(context.Background(), nil, &TurnResult{}) }()
			if tt.called {
				response := readMessage(t, reader)
				if string(response["result"]) != `{"outcome":{"outcome":"selected","optionId":"yes"}}` {
					t.Fatalf("permission response: %s", response["result"])
				}
			}
			awaitError(t, done, ErrConfiguration)
			if called != tt.called {
				t.Fatalf("policy called = %v, want %v", called, tt.called)
			}
		})
	}
}

func TestReviewSessionNotificationCapacity(t *testing.T) {
	t.Parallel()
	conn, peer := testConnection(t, Options{NotificationBuffer: 1})
	cwd := tempDir(t)
	done := make(chan error, 1)
	go func() {
		_, err := NewSession(context.Background(), conn, SessionConfig{Cwd: cwd})
		done <- err
	}()
	reader := bufio.NewReader(peer)
	setupPeer(t, peer, reader, cwd, `{}`)
	awaitError(t, done, nil)
	update, err := decodeMessage([]byte(configUpdate(configState("large", "high"))))
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.receive(update); err != nil {
		t.Fatal(err)
	}
	if err := conn.receive(update); !errors.Is(err, ErrCapacity) {
		t.Fatalf("notification overflow: %v, want %v", err, ErrCapacity)
	}
}
