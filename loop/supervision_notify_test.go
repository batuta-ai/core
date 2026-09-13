package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type supervisionSinkFunc func(context.Context, SupervisionNotification) error

func (f supervisionSinkFunc) Notify(ctx context.Context, event SupervisionNotification) error {
	return f(ctx, event)
}

func TestSupervisionNotificationOutbox(t *testing.T) {
	for _, mode := range []string{"missing", "failed", "success"} {
		t.Run(mode, func(t *testing.T) {
			store, observer := supervisionFixture(t)
			supervisionAppend(t, store, observer, KindOpened, `{}`)
			supervisionAppend(t, store, observer, KindQuestion, `{"execution":1,"request_id":"q1"}`)
			supervisionAppend(t, store, observer, KindTerminal, `{"state":"waiting_input"}`)
			var output bytes.Buffer
			calls := 0
			opts := SuperviseOptions{Observer: observer, Interval: time.Second, Once: true, Output: &output}
			if mode != "missing" {
				opts.Sink = supervisionSinkFunc(func(ctx context.Context, notification SupervisionNotification) error {
					calls++
					if notification.State != StateWaitingInput || notification.Event.Delivery != observer.Delivery || notification.Event.ID == "" {
						t.Fatalf("notification=%+v", notification)
					}
					if _, ok := ctx.Deadline(); !ok {
						t.Fatal("unbounded sink invocation")
					}
					if mode == "failed" {
						return errors.New("secret sink error")
					}
					return nil
				})
			}
			err := Supervise(context.Background(), opts)
			if (err != nil) != (mode == "failed") {
				t.Fatalf("err=%v", err)
			}
			pending := supervisionObserve(t, observer).Pending
			want := 2
			if mode == "success" {
				want = 0
			}
			if len(pending) != want {
				t.Fatalf("pending=%+v", pending)
			}
			status := map[string]string{"missing": "unconfigured", "failed": "failed", "success": "acknowledged"}[mode]
			if !strings.Contains(output.String(), `"notification":"`+status+`"`) || strings.Contains(output.String(), "secret") || strings.Contains(output.String(), "chat") {
				t.Fatalf("output=%s", &output)
			}
			if mode == "success" {
				if err := Supervise(context.Background(), opts); err != nil {
					t.Fatal(err)
				}
				if calls != 2 {
					t.Fatalf("acknowledged notifications resent: %d", calls)
				}
			}
		})
	}
}

func TestSupervisionFileSinkRestartAndCompletion(t *testing.T) {
	store, observer := supervisionFixture(t)
	supervisionAppend(t, store, observer, KindTerminal, `{"state":"done"}`)
	directory := t.TempDir()
	sink := SupervisionFileSink{Directory: directory}
	observation := supervisionObserve(t, observer)
	notification := SupervisionNotification{Event: observation.Events[0], State: StateDone, Presence: observation.Presence}
	// Simulate a crash after delivery but before outbox acknowledgment.
	if err := sink.Notify(context.Background(), notification); err != nil {
		t.Fatal(err)
	}
	if err := Supervise(context.Background(), SuperviseOptions{Observer: observer, Interval: time.Second, Sink: sink,
		Sleep: func(context.Context, time.Duration) error { t.Fatal("completed delivery slept"); return nil },
	}); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(directory)
	if err != nil || len(files) != 1 {
		t.Fatalf("files=%v err=%v", files, err)
	}
	data, err := os.ReadFile(filepath.Join(directory, files[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	var got SupervisionNotification
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Event.Completed || got.Event.ID != notification.Event.ID || got.State != StateDone || len(data) > SupervisionEventLimit {
		t.Fatalf("notification=%s", data)
	}
	if pending := supervisionObserve(t, observer).Pending; len(pending) != 0 {
		t.Fatalf("pending=%v", pending)
	}
}

func TestSupervisionDesktopUsesArguments(t *testing.T) {
	event := SupervisionNotification{Event: SupervisionEvent{ID: "demo:1", Delivery: "demo", Action: `$(touch /tmp/unwanted); "quoted"`}, State: "open"}
	for _, platform := range []string{"darwin", "linux", "unsupported"} {
		calls := 0
		sink := supervisionDesktopSink{platform: platform, run: func(ctx context.Context, name string, args ...string) error {
			calls++
			if name == "sh" || name == "bash" {
				t.Fatal("shell invocation")
			}
			if !strings.Contains(args[len(args)-1], event.Event.Action) {
				t.Fatalf("event not passed as data: %q", args)
			}
			if platform == "darwin" && strings.Contains(args[1], event.Event.Action) {
				t.Fatal("event interpolated in script")
			}
			return nil
		}}
		err := sink.Notify(context.Background(), event)
		if platform == "unsupported" {
			if err == nil || calls != 0 {
				t.Fatalf("unsupported: %v calls=%d", err, calls)
			}
		} else if err != nil || calls != 1 {
			t.Fatalf("%s: %v calls=%d", platform, err, calls)
		}
	}
}

func TestSupervisionForegroundPolicyIndependentOfNotification(t *testing.T) {
	store, observer, event, policy := supervisionPolicyFixture(t)
	if err := AcknowledgeSupervision(observer, event.ID); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := Supervise(context.Background(), SuperviseOptions{Observer: observer, Interval: time.Second, Once: true, Policy: &policy, Output: &output}); err != nil {
		t.Fatal(err)
	}
	records, err := store.Read(observer.Delivery)
	if err != nil || records[len(records)-1].Kind != KindAnswer {
		t.Fatalf("answer missing: %v %v", records, err)
	}
	if !strings.Contains(output.String(), `"outcome":"answered"`) {
		t.Fatalf("decision missing: %s", &output)
	}
}

func TestSupervisionOutputIsCompactWithOverflowReference(t *testing.T) {
	store, observer := supervisionFixture(t)
	for range 40 {
		supervisionAppend(t, store, observer, KindQuestion, `{"request_id":"q"}`)
	}
	var output bytes.Buffer
	if err := Supervise(context.Background(), SuperviseOptions{Observer: observer, Interval: time.Second, Once: true, Output: &output}); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		if len(line) > SupervisionEventLimit {
			t.Fatalf("output line exceeds 4 KiB: %d bytes", len(line))
		}
	}
	if !strings.Contains(output.String(), observer.CursorPath) || !strings.Contains(output.String(), `"pending":40`) {
		t.Fatalf("missing overflow reference: %s", &output)
	}
}

func TestSupervisionNotificationRetry(t *testing.T) {
	store, observer := supervisionFixture(t)
	supervisionAppend(t, store, observer, KindTerminal, `{"state":"done"}`)
	calls, waits := 0, 0
	var output bytes.Buffer
	err := Supervise(context.Background(), SuperviseOptions{Observer: observer, Interval: time.Second, Output: &output,
		Sink: supervisionSinkFunc(func(context.Context, SupervisionNotification) error {
			calls++
			if calls == 1 {
				return errors.New("unavailable")
			}
			return nil
		}),
		Sleep: func(context.Context, time.Duration) error { waits++; return nil },
	})
	if err != nil || calls != 2 || waits != 1 {
		t.Fatalf("calls=%d waits=%d err=%v", calls, waits, err)
	}
	if !strings.Contains(output.String(), `"notification":"failed"`) || !strings.Contains(output.String(), `"notification":"acknowledged"`) {
		t.Fatalf("output=%s", &output)
	}
}

func TestSupervisionPolicyFileIsBoundedAndStrict(t *testing.T) {
	_, _, _, policy := supervisionPolicyFixture(t)
	valid, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{string(valid), `null`, `{}`, `{"unknown":true}`, string(valid) + `{}`, strings.Repeat(" ", SupervisionEventLimit+1)} {
		path := filepath.Join(t.TempDir(), "policy.json")
		if err := os.WriteFile(path, []byte(payload), 0600); err != nil {
			t.Fatal(err)
		}
		got, err := LoadSupervisionPolicy(path)
		if payload == string(valid) {
			if err != nil || got.Delivery != policy.Delivery {
				t.Fatalf("policy=%v err=%v", got, err)
			}
		} else if err == nil {
			t.Fatalf("accepted invalid policy %q", payload)
		}
	}
}

func TestSupervisionCancellationPreventsIntervention(t *testing.T) {
	store, observer, _, policy := supervisionPolicyFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	before, err := store.Read(observer.Delivery)
	if err != nil {
		t.Fatal(err)
	}
	err = Supervise(ctx, SuperviseOptions{Observer: observer, Interval: time.Second, Once: true, Policy: &policy,
		Sink: supervisionSinkFunc(func(context.Context, SupervisionNotification) error { cancel(); return context.Canceled }),
	})
	if err != nil {
		t.Fatal(err)
	}
	after, err := store.Read(observer.Delivery)
	if err != nil || len(after) != len(before) {
		t.Fatalf("intervened after cancellation: records=%d before=%d err=%v", len(after), len(before), err)
	}
}

func TestSupervisionReviewNotificationRetryAndPolicyLoad(t *testing.T) {
	observer, event, policy := supervisionCorrectionFixture(t)
	filename := filepath.Join(observer.Workspace, "policy.json")
	if err := writeSupervisionJSON(filename, policy); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadSupervisionPolicy(filename)
	if err != nil || loaded.Correction.ReviewID != event.ReviewID {
		t.Fatalf("policy: %+v %v", loaded, err)
	}
	calls := 0
	opts := SuperviseOptions{Observer: observer, Interval: 100 * time.Millisecond, Once: true, Sink: supervisionSinkFunc(func(_ context.Context, n SupervisionNotification) error {
		if n.Event.Kind == "review" {
			calls++
			if n.Event.ReviewOutcome != "FIX_BEFORE_SHIP" || n.Event.Evidence.Path == "" {
				t.Fatalf("notification: %+v", n)
			}
			if calls == 1 {
				return errors.New("sink failed")
			}
		}
		return nil
	})}
	if err := Supervise(context.Background(), opts); err == nil {
		t.Fatal("missing sink failure")
	}
	if err := Supervise(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(supervisionObserve(t, observer).Pending) != 0 {
		t.Fatalf("notification retry calls=%d", calls)
	}
	var message string
	desktop := supervisionDesktopSink{platform: "linux", run: func(_ context.Context, _ string, args ...string) error { message = strings.Join(args, " "); return nil }}
	if err := desktop.Notify(context.Background(), SupervisionNotification{Event: event, State: "done"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(message, "FIX_BEFORE_SHIP") || !strings.Contains(message, event.Evidence.Path) {
		t.Fatalf("desktop omitted outcome: %s", message)
	}
}
