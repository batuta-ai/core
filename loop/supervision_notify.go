package loop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// SupervisionNotification carries the historical event and the observed delivery
// state. An executor exit alone never establishes delivery completion.
type SupervisionNotification struct {
	Event    SupervisionEvent `json:"event"`
	State    string           `json:"state"`
	Presence string           `json:"presence"`
}

// SupervisionSink returns nil only after local delivery succeeds. Notifications
// can be repeated after a crash before acknowledgment; sinks should deduplicate
// by Event.ID. Acceptance by a desktop service does not prove a human read it.
type SupervisionSink interface {
	Notify(context.Context, SupervisionNotification) error
}

// SupervisionFileSink writes one private JSON file per stable event ID into an
// existing local directory. Re-delivery replaces the same file atomically.
// Reading these files is the host's responsibility; this does not wake a chat.
type SupervisionFileSink struct {
	Directory string
}

func (sink SupervisionFileSink) Notify(ctx context.Context, event SupervisionNotification) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !filepath.IsAbs(sink.Directory) {
		return errors.New("loop: notification directory must be absolute")
	}
	if _, err := encodeSupervisionNotification(event); err != nil {
		return err
	}
	name := fmt.Sprintf("%x.json", sha256.Sum256([]byte(event.Event.ID)))
	return writeSupervisionJSON(filepath.Join(sink.Directory, name), event)
}

// NewSupervisionDesktopSink uses the installed local OS notification facility.
// It installs nothing and does not configure a remote destination. Unsupported
// hosts or missing commands fail delivery and leave the durable event unread.
func NewSupervisionDesktopSink() SupervisionSink {
	return supervisionDesktopSink{platform: runtime.GOOS, run: func(ctx context.Context, name string, args ...string) error {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.WaitDelay = time.Second
		return cmd.Run()
	}}
}

type supervisionDesktopSink struct {
	platform string
	run      func(context.Context, string, ...string) error
}

func (sink supervisionDesktopSink) Notify(ctx context.Context, event SupervisionNotification) error {
	if _, err := encodeSupervisionNotification(event); err != nil {
		return err
	}
	title := "Batuta delivery " + event.Event.Delivery
	message := fmt.Sprintf("%s | state: %s | event: %s\ncompleted: %t; recovery pending: %t\n%s\nEvidence: %s (sequence %d)",
		event.Event.ID, event.State, event.Event.Kind, event.Event.Completed, event.Event.RecoveryPending,
		event.Event.Action, event.Event.Evidence.Path, event.Event.Evidence.Sequence)
	if event.Event.Kind == "review" {
		message = fmt.Sprintf("Review %s | outcome: %s | state: %s\n%s\nEvidence: %s", event.Event.ReviewID, event.Event.ReviewOutcome, event.Event.ReviewState, event.Event.Action, event.Event.Evidence.Path)
	}
	switch sink.platform {
	case "darwin":
		return sink.run(ctx, "osascript", "-e", "on run argv\ndisplay notification (item 2 of argv) with title (item 1 of argv)\nend run", "--", title, message)
	case "linux":
		return sink.run(ctx, "notify-send", "--", title, message)
	default:
		return errors.New("loop: desktop notifications are unsupported on this host")
	}
}

func encodeSupervisionNotification(event SupervisionNotification) ([]byte, error) {
	data, err := json.Marshal(event)
	if err != nil {
		return nil, err
	}
	if event.Event.ID == "" || len(data) > SupervisionEventLimit {
		return nil, errors.New("loop: notification lacks an event ID or exceeds 4 KiB")
	}
	return data, nil
}

// LoadSupervisionPolicy reads a bounded, explicit operator policy, rejecting
// unknown fields so a misspelled constraint cannot silently disappear.
func LoadSupervisionPolicy(path string) (*SupervisionPolicy, error) {
	data, err := readSupervisionFile(path, SupervisionEventLimit)
	if err != nil {
		return nil, err
	}
	var policy SupervisionPolicy
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("loop: supervision policy must contain one JSON object")
	}
	if policy.Action == SupervisionProposeCorrection && policy.Delivery != "" && policy.Correction != nil {
		return &policy, nil
	}
	if policy.Delivery == "" || policy.TaskID == "" || policy.QuestionID == "" {
		return nil, errors.New("loop: supervision policy requires delivery, task and question identity")
	}
	return &policy, nil
}
