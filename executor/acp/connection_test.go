package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func testConnection(t *testing.T, options Options) (*Connection, net.Conn) {
	t.Helper()
	client, peer := net.Pipe()
	if options.RequestTimeout == 0 {
		options.RequestTimeout = 2 * time.Second
	}
	conn, err := NewConnection(client, client, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		peer.Close()
		conn.Close()
		select {
		case <-conn.Done():
		case <-time.After(3 * time.Second):
			t.Error("connection goroutines did not stop")
		}
	})
	if err := peer.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	return conn, peer
}

func readMessage(t *testing.T, reader *bufio.Reader) map[string]json.RawMessage {
	t.Helper()
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var message map[string]json.RawMessage
	if err := json.Unmarshal(line, &message); err != nil {
		t.Fatal(err)
	}
	return message
}

func writeMessage(t *testing.T, peer io.Writer, message string) {
	t.Helper()
	if _, err := io.WriteString(peer, message+"\n"); err != nil {
		t.Fatal(err)
	}
}

func awaitError(t *testing.T, result <-chan error, want error) {
	t.Helper()
	select {
	case err := <-result:
		if !errors.Is(err, want) {
			t.Fatalf("error = %v, want %v", err, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("operation did not terminate")
	}
}

func TestInitializeNegotiatesVersionAndCapabilities(t *testing.T) {
	t.Parallel()
	conn, peer := testConnection(t, Options{})
	result := make(chan error, 1)
	go func() {
		initialized, err := conn.Initialize(context.Background(), Implementation{Name: "batuta", Version: "test"})
		if err == nil && (initialized.ProtocolVersion != 1 || !initialized.AgentCapabilities.LoadSession || !initialized.AgentCapabilities.PromptCapabilities.Image || initialized.AgentInfo.Name != "fixture" || len(initialized.AuthMethods) != 1) {
			err = fmt.Errorf("unexpected negotiation: %+v", initialized)
		}
		result <- err
	}()
	request := readMessage(t, bufio.NewReader(peer))
	if string(request["method"]) != `"initialize"` {
		t.Fatalf("method: %s", request["method"])
	}
	var params map[string]json.RawMessage
	if err := json.Unmarshal(request["params"], &params); err != nil {
		t.Fatal(err)
	}
	if string(params["protocolVersion"]) != "1" || string(params["clientCapabilities"]) != "{}" {
		t.Fatalf("advertised unimplemented capabilities: %s", request["params"])
	}
	writeMessage(t, peer, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"agentCapabilities":{"loadSession":true,"promptCapabilities":{"image":true},"future":{}},"agentInfo":{"name":"fixture","version":"1"},"authMethods":[{"id":"local","name":"Local"}]}}`, request["id"]))
	awaitError(t, result, nil)
	if _, err := conn.Initialize(context.Background(), Implementation{}); !errors.Is(err, ErrAlreadyInitialized) {
		t.Fatalf("second initialize: %v", err)
	}
}

func TestInitializeRejectsUnsupportedVersion(t *testing.T) {
	t.Parallel()
	conn, peer := testConnection(t, Options{})
	result := make(chan error, 1)
	go func() { _, err := conn.Initialize(context.Background(), Implementation{}); result <- err }()
	request := readMessage(t, bufio.NewReader(peer))
	writeMessage(t, peer, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":2}}`, request["id"]))
	awaitError(t, result, ErrVersion)
	<-conn.Done()
}

func TestConcurrentCallsAndInterleavedNotifications(t *testing.T) {
	t.Parallel()
	conn, peer := testConnection(t, Options{})
	const count = 8
	result := make(chan error, count)
	for i := 0; i < count; i++ {
		go func() {
			want := fmt.Sprintf(`{"value":%d}`, i)
			got, err := conn.Call(context.Background(), "echo", json.RawMessage(want))
			if err == nil && string(got) != want {
				err = fmt.Errorf("response %s, want %s", got, want)
			}
			result <- err
		}()
	}
	reader := bufio.NewReader(peer)
	requests := make([]map[string]json.RawMessage, count)
	ids := map[string]bool{}
	for i := range requests {
		requests[i] = readMessage(t, reader)
		id := string(requests[i]["id"])
		if ids[id] {
			t.Fatal("duplicate id")
		}
		ids[id] = true
	}
	writeMessage(t, peer, `{"jsonrpc":"2.0","method":"_future/update","params":{}}`)
	for i := count - 1; i >= 0; i-- {
		writeMessage(t, peer, fmt.Sprintf(`{"jsonrpc":"2.0","method":"session/update","params":{"sequence":%d}}`, i))
		writeMessage(t, peer, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":%s}`, requests[i]["id"], requests[i]["params"]))
	}
	for i := 0; i < count; i++ {
		awaitError(t, result, nil)
	}
	for i := count - 1; i >= 0; i-- {
		select {
		case notification := <-conn.Notifications():
			if notification.Method != "session/update" || string(notification.Params) != fmt.Sprintf(`{"sequence":%d}`, i) {
				t.Fatalf("notification: %+v", notification)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("notification missing")
		}
	}
}

func TestInvalidFramesTerminatePendingCalls(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"malformed": `{"jsonrpc":`, "oversized": strings.Repeat("x", 257),
		"batch": `[]`, "version": `{"jsonrpc":"1.0","id":1,"result":{}}`,
		"both outcomes":   `{"jsonrpc":"2.0","id":1,"result":{},"error":{"code":1,"message":"bad"}}`,
		"missing outcome": `{"jsonrpc":"2.0","id":1}`, "null id": `{"jsonrpc":"2.0","id":null,"result":{}}`,
		"unknown id":        `{"jsonrpc":"2.0","id":999,"result":{}}`,
		"scalar params":     `{"jsonrpc":"2.0","method":"session/update","params":true}`,
		"duplicate keys":    `{"jsonrpc":"2.0","id":1,"id":2,"result":{}}`,
		"invalid utf8":      "{\"jsonrpc\":\"2.0\",\"method\":\"session/update\",\"params\":{\"text\":\"\xff\"}}",
		"wrong version key": `{"JSONRPC":"2.0","id":1,"result":{}}`,
		"invalid error":     `{"jsonrpc":"2.0","id":1,"error":{"message":"secret"}}`,
	}
	for name, frame := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			conn, peer := testConnection(t, Options{MaxFrameBytes: 256})
			result := make(chan error, 1)
			go func() { _, err := conn.Call(context.Background(), "echo", nil); result <- err }()
			request := readMessage(t, bufio.NewReader(peer))
			frame = strings.ReplaceAll(frame, `"id":1`, `"id":`+string(request["id"]))
			// A rejected oversized frame can close the pipe before its newline is consumed.
			_, _ = io.WriteString(peer, frame+"\n")
			want := ErrProtocol
			if name == "oversized" {
				want = ErrFrameTooLarge
			}
			awaitError(t, result, want)
		})
	}
}

func TestEOFAndPartialFrameTerminate(t *testing.T) {
	t.Parallel()
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprint(partial), func(t *testing.T) {
			t.Parallel()
			conn, peer := testConnection(t, Options{})
			result := make(chan error, 1)
			go func() { _, err := conn.Call(context.Background(), "echo", nil); result <- err }()
			readMessage(t, bufio.NewReader(peer))
			if partial {
				_, _ = io.WriteString(peer, `{"jsonrpc":"2.0"`)
			}
			peer.Close()
			want := io.EOF
			if partial {
				want = ErrProtocol
			}
			awaitError(t, result, want)
		})
	}
}

func TestMissingReplyAndBlockedWriteHaveDefaultDeadline(t *testing.T) {
	t.Parallel()
	for _, read := range []bool{false, true} {
		t.Run(fmt.Sprint(read), func(t *testing.T) {
			t.Parallel()
			conn, peer := testConnection(t, Options{RequestTimeout: 25 * time.Millisecond})
			result := make(chan error, 1)
			go func() { _, err := conn.Call(context.Background(), "echo", nil); result <- err }()
			if read {
				readMessage(t, bufio.NewReader(peer))
			}
			awaitError(t, result, context.DeadlineExceeded)
			_, err := conn.Call(context.Background(), "no-replay", nil)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("terminal error lost: %v", err)
			}
		})
	}
}

func TestUnknownRequestsAreRejected(t *testing.T) {
	t.Parallel()
	conn, peer := testConnection(t, Options{})
	reader := bufio.NewReader(peer)
	for _, id := range []string{`"remote"`, `7`} {
		writeMessage(t, peer, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"method":"fs/write_text_file","params":{"content":"secret"}}`, id))
		response := readMessage(t, reader)
		if string(response["id"]) != id || string(response["error"]) != `{"code":-32601,"message":"Method not found"}` {
			t.Fatalf("rejection: %s", response)
		}
	}
	if err := conn.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestNotificationOverflowIsVisible(t *testing.T) {
	t.Parallel()
	conn, peer := testConnection(t, Options{NotificationBuffer: 1})
	writeMessage(t, peer, `{"jsonrpc":"2.0","method":"session/update","params":{}}`)
	writeMessage(t, peer, `{"jsonrpc":"2.0","method":"session/update","params":{}}`)
	select {
	case <-conn.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("overflow did not terminate")
	}
	if !errors.Is(conn.Err(), ErrCapacity) {
		t.Fatalf("overflow: %v", conn.Err())
	}
}

func TestPendingCallsAreBounded(t *testing.T) {
	t.Parallel()
	conn, peer := testConnection(t, Options{MaxPending: 1})
	result := make(chan error, 1)
	go func() { _, err := conn.Call(context.Background(), "first", nil); result <- err }()
	readMessage(t, bufio.NewReader(peer))
	_, err := conn.Call(context.Background(), "second", nil)
	if !errors.Is(err, ErrCapacity) {
		t.Fatalf("capacity: %v", err)
	}
	conn.Close()
	awaitError(t, result, ErrClosed)
}

func TestOutboundValidationAndNotification(t *testing.T) {
	t.Parallel()
	conn, peer := testConnection(t, Options{MaxFrameBytes: 128})
	for _, params := range []json.RawMessage{json.RawMessage(`true`), json.RawMessage(`{`), json.RawMessage(`{"text":"` + strings.Repeat("x", 128) + `"}`)} {
		if _, err := conn.Call(context.Background(), "echo", params); err == nil {
			t.Fatal("invalid outbound payload accepted")
		}
	}
	result := make(chan error, 1)
	go func() {
		result <- conn.Notify(context.Background(), "session/cancel", json.RawMessage(`{"sessionId":"s"}`))
	}()
	message := readMessage(t, bufio.NewReader(peer))
	if _, ok := message["id"]; ok || string(message["method"]) != `"session/cancel"` {
		t.Fatalf("notification: %s", message)
	}
	awaitError(t, result, nil)
}

func TestRPCErrorDoesNotExposePeerText(t *testing.T) {
	t.Parallel()
	conn, peer := testConnection(t, Options{})
	result := make(chan error, 1)
	go func() { _, err := conn.Call(context.Background(), "echo", nil); result <- err }()
	request := readMessage(t, bufio.NewReader(peer))
	writeMessage(t, peer, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":{"code":-32000,"message":"credential-canary","data":{"token":"credential-canary"}}}`, request["id"]))
	err := <-result
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != -32000 || strings.Contains(fmt.Sprint(err), "credential-canary") {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestPermissionRequestCanBeAnswered(t *testing.T) {
	t.Parallel()
	conn, peer := testConnection(t, Options{})
	writeMessage(t, peer, `{"jsonrpc":"2.0","id":"permission","method":"session/request_permission","params":{"sessionId":"s","options":[]}}`)
	request := <-conn.Requests()
	if request.Method != "session/request_permission" {
		t.Fatalf("request: %+v", request)
	}
	result := make(chan error, 1)
	go func() {
		result <- conn.Respond(context.Background(), request.ID, json.RawMessage(`{"outcome":{"outcome":"cancelled"}}`), nil)
	}()
	response := readMessage(t, bufio.NewReader(peer))
	if string(response["id"]) != `"permission"` || string(response["result"]) != `{"outcome":{"outcome":"cancelled"}}` {
		t.Fatalf("permission response: %s", response)
	}
	awaitError(t, result, nil)
}

func TestUnansweredPermissionHasDeadline(t *testing.T) {
	t.Parallel()
	conn, peer := testConnection(t, Options{RequestTimeout: 25 * time.Millisecond})
	writeMessage(t, peer, `{"jsonrpc":"2.0","id":1,"method":"session/request_permission","params":{}}`)
	select {
	case <-conn.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("permission deadline did not terminate")
	}
	if !errors.Is(conn.Err(), context.DeadlineExceeded) {
		t.Fatalf("permission timeout: %v", conn.Err())
	}
}

func TestReplyImmediatelyBeforeEOFSucceeds(t *testing.T) {
	t.Parallel()
	for attempt := 0; attempt < 100; attempt++ {
		conn, peer := testConnection(t, Options{})
		result := make(chan error, 1)
		go func() {
			got, err := conn.Call(context.Background(), "echo", nil)
			if err == nil && string(got) != `{}` {
				err = fmt.Errorf("result: %s", got)
			}
			result <- err
		}()
		request := readMessage(t, bufio.NewReader(peer))
		writeMessage(t, peer, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{}}`, request["id"]))
		peer.Close()
		awaitError(t, result, nil)
		conn.Close()
	}
}

func TestContextCancellationTerminatesAllPendingCalls(t *testing.T) {
	t.Parallel()
	conn, peer := testConnection(t, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make(chan error, 2)
	go func() { _, err := conn.Call(ctx, "first", nil); results <- err }()
	reader := bufio.NewReader(peer)
	readMessage(t, reader)
	go func() { _, err := conn.Call(context.Background(), "second", nil); results <- err }()
	readMessage(t, reader)
	cancel()
	awaitError(t, results, context.Canceled)
	awaitError(t, results, context.Canceled)
}

func TestCancelledContextDoesNotSubmit(t *testing.T) {
	t.Parallel()
	conn, _ := testConnection(t, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := conn.Call(ctx, "echo", nil)
	if !errors.Is(err, context.Canceled) || conn.Err() != nil {
		t.Fatalf("pre-submission cancel: %v / %v", err, conn.Err())
	}
}

func TestUnknownRequestWithUnreadReplyTerminates(t *testing.T) {
	t.Parallel()
	conn, peer := testConnection(t, Options{RequestTimeout: 25 * time.Millisecond})
	writeMessage(t, peer, `{"jsonrpc":"2.0","id":1,"method":"unsupported"}`)
	select {
	case <-conn.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("unread rejection blocked connection")
	}
	if !errors.Is(conn.Err(), context.DeadlineExceeded) {
		t.Fatalf("unread reply: %v", conn.Err())
	}
}

func TestPermissionCapacityAndDuplicateIDs(t *testing.T) {
	t.Parallel()
	for _, id := range []int{1, 2} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			t.Parallel()
			conn, peer := testConnection(t, Options{MaxPending: 1})
			writeMessage(t, peer, `{"jsonrpc":"2.0","id":1,"method":"session/request_permission","params":{}}`)
			// Reading the queue must not release the outstanding request's capacity.
			select {
			case <-conn.Requests():
			case <-time.After(3 * time.Second):
				t.Fatal("request missing")
			}
			writeMessage(t, peer, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"session/request_permission","params":{}}`, id))
			select {
			case <-conn.Done():
			case <-time.After(3 * time.Second):
				t.Fatal("request limit did not terminate")
			}
			want := ErrCapacity
			if id == 1 {
				want = ErrProtocol
			}
			if !errors.Is(conn.Err(), want) {
				t.Fatalf("request error: %v", conn.Err())
			}
		})
	}
}

func TestFrameBoundaryAndFragmentedCRLF(t *testing.T) {
	t.Parallel()
	base := `{"jsonrpc":"2.0","method":"session/update","params":{"text":"` + strings.Repeat("x", 5000) + `"}}`
	conn, peer := testConnection(t, Options{MaxFrameBytes: len(base) + 1})
	for _, chunk := range []string{base[:37], base[37:4095], base[4095:] + "\r\n"} {
		if _, err := io.WriteString(peer, chunk); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case got := <-conn.Notifications():
		if len(got.Params) != 5011 {
			t.Fatalf("params length: %d", len(got.Params))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("fragmented notification missing")
	}
}

func TestOptionsCannotIncreaseMemoryCeilings(t *testing.T) {
	t.Parallel()
	for _, options := range []Options{{MaxFrameBytes: -1}, {MaxFrameBytes: 1<<20 + 1}, {MaxPending: 33}, {NotificationBuffer: 17}, {RequestTimeout: -1}} {
		client, peer := net.Pipe()
		conn, err := NewConnection(client, client, options)
		client.Close()
		peer.Close()
		if conn != nil || !errors.Is(err, ErrOptions) {
			t.Fatalf("options %+v: %v", options, err)
		}
	}
}

// The child executes only this test, using real stdin/stdout OS pipes.
func TestStdioPeer(t *testing.T) {
	scenario := os.Getenv("BATUTA_ACP_TEST_PEER")
	if scenario == "" {
		return
	}
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		os.Exit(2)
	}
	var request map[string]json.RawMessage
	if json.Unmarshal(line, &request) != nil {
		os.Exit(3)
	}
	switch scenario {
	case "initialize":
		fmt.Fprintf(os.Stdout, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"agentCapabilities":{}}}`+"\n", request["id"])
		_, _ = io.Copy(io.Discard, os.Stdin)
	case "oversized":
		_, _ = io.WriteString(os.Stdout, strings.Repeat("x", 2048))
	case "missing":
		_, _ = io.Copy(io.Discard, os.Stdin)
	default:
		os.Exit(4)
	}
	os.Exit(0)
}

func TestRealStdioChild(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"initialize", "oversized", "missing"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, executable, "-test.run=^TestStdioPeer$")
			command.Env = append(os.Environ(), "BATUTA_ACP_TEST_PEER="+scenario)
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
			conn, err := NewConnection(output, input, Options{MaxFrameBytes: 1024, RequestTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			_, callErr := conn.Initialize(ctx, Implementation{Name: "batuta", Version: "test"})
			conn.Close()
			waitErr := command.Wait()
			if ctx.Err() != nil {
				t.Fatal("child required hard deadline")
			}
			want := error(nil)
			if scenario == "oversized" {
				want = ErrFrameTooLarge
			}
			if scenario == "missing" {
				want = context.DeadlineExceeded
			}
			if !errors.Is(callErr, want) {
				t.Fatalf("initialize: %v, want %v", callErr, want)
			}
			// Oversized output may encounter a closed pipe; other peers exit cleanly on EOF.
			if scenario != "oversized" && waitErr != nil {
				t.Fatalf("child exit: %v", waitErr)
			}
		})
	}
}

func TestOversizedFrameWithoutNewlineOrPendingCallTerminates(t *testing.T) {
	t.Parallel()
	conn, peer := testConnection(t, Options{MaxFrameBytes: 5000})
	_, _ = io.WriteString(peer, strings.Repeat("x", 5001))
	select {
	case <-conn.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("oversized unterminated frame was not rejected")
	}
	if !errors.Is(conn.Err(), ErrFrameTooLarge) {
		t.Fatalf("frame error: %v", conn.Err())
	}
}

func TestPromptLifetimeAndControlDeadlines(t *testing.T) {
	for _, tt := range []struct {
		name, method string
		read, reply  bool
	}{
		{"prompt survives control timeout", "session/prompt", true, true},
		{"prompt task deadline", "session/prompt", true, false},
		{"prompt blocked write", "session/prompt", false, false},
		{"missing control reply", "session/new", true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				conn, peer := testConnection(t, Options{RequestTimeout: 100 * time.Millisecond})
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				done := make(chan error, 1)
				go func() {
					_, err := conn.Call(ctx, tt.method, nil)
					done <- err
				}()
				var request map[string]json.RawMessage
				if tt.read {
					request = expectMethod(t, bufio.NewReader(peer), tt.method)
				}
				<-time.NewTimer(200 * time.Millisecond).C
				synctest.Wait()
				if tt.method == "session/prompt" && tt.read {
					if err := conn.Err(); err != nil {
						t.Fatalf("prompt terminated at control timeout: %v", err)
					}
					if tt.reply {
						sessionReply(t, peer, request, `{"stopReason":"end_turn"}`)
						awaitError(t, done, nil)
						return
					}
					<-ctx.Done()
					synctest.Wait()
				}
				select {
				case err := <-done:
					if !errors.Is(err, context.DeadlineExceeded) {
						t.Fatalf("deadline error: %v", err)
					}
				default:
					t.Fatal("operation exceeded its deadline")
				}
				<-conn.Done()
			})
		})
	}
}

func TestPendingPromptAllowsPermissionAndCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		conn, peer := testConnection(t, Options{MaxPending: 1})
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		done := make(chan error, 1)
		go func() {
			_, err := conn.Call(ctx, "session/prompt", nil)
			done <- err
		}()
		reader := bufio.NewReader(peer)
		prompt := expectMethod(t, reader, "session/prompt")
		synctest.Wait()
		if _, err := conn.Call(ctx, "second", nil); !errors.Is(err, ErrCapacity) {
			t.Fatalf("pending call limit: %v", err)
		}
		writeMessage(t, peer, `{"jsonrpc":"2.0","id":"p","method":"session/request_permission","params":`+permissionParams+`}`)
		request := <-conn.Requests()
		written := make(chan error, 1)
		go func() {
			written <- conn.Respond(ctx, request.ID, json.RawMessage(`{"outcome":{"outcome":"selected","optionId":"yes"}}`), nil)
		}()
		synctest.Wait()
		select {
		case err := <-written:
			t.Fatalf("permission reply could not reach peer: %v", err)
		default:
		}
		response := readMessage(t, reader)
		if string(response["id"]) != `"p"` || string(response["result"]) != `{"outcome":{"outcome":"selected","optionId":"yes"}}` {
			t.Fatalf("permission response: %s", response)
		}
		awaitError(t, written, nil)
		go func() {
			written <- conn.Notify(ctx, "session/cancel", json.RawMessage(`{"sessionId":"task"}`))
		}()
		synctest.Wait()
		select {
		case err := <-written:
			t.Fatalf("cancellation could not reach peer: %v", err)
		default:
		}
		notification := expectMethod(t, reader, "session/cancel")
		if len(notification["id"]) != 0 {
			t.Fatalf("cancel must be a notification: %s", notification)
		}
		awaitError(t, written, nil)
		sessionReply(t, peer, prompt, `{"stopReason":"cancelled"}`)
		awaitError(t, done, nil)
	})
}

func TestControlWritesRemainBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		conn, _ := testConnection(t, Options{MaxPending: 1, RequestTimeout: 100 * time.Millisecond})
		done := make(chan error, 1)
		go func() { done <- conn.Notify(context.Background(), "session/cancel", nil) }()
		synctest.Wait()
		if err := conn.Notify(context.Background(), "session/cancel", nil); !errors.Is(err, ErrCapacity) {
			t.Fatalf("control write limit: %v", err)
		}
		<-time.NewTimer(100 * time.Millisecond).C
		synctest.Wait()
		awaitError(t, done, context.DeadlineExceeded)
		<-conn.Done()
	})
}
