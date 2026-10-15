package loop

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"

	"github.com/batuta-ai/core/journal"
)

type supervisionGateResult struct {
	gate SupervisionGate
	err  error
}

func supervisionSnapshotFixture(t *testing.T) (*journal.Store, SupervisionOptions) {
	t.Helper()
	_, opts := supervisionFixture(t)
	root, store, err := openStore(opts.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	opts.Workspace = root
	opts.CursorPath = filepath.Join(root, "observer.json")
	return store, opts
}

func supervisionSnapshot(t *testing.T, store *journal.Store, opts SupervisionOptions) journal.Snapshot {
	t.Helper()
	file, err := os.Open(store.Path(opts.Delivery))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := file.Close(); err != nil {
			t.Errorf("close borrowed snapshot descriptor: %v", err)
		}
	})
	snapshot, err := journal.ReadSnapshot(context.Background(), file)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func supervisionJournalOwner(t *testing.T, path string) (*os.File, func() error) {
	t.Helper()
	owner, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := lockSupervisionJournalForTest(owner)
	if err != nil {
		_ = owner.Close()
		t.Fatal(err)
	}
	released := false
	release := func() error {
		if released {
			return nil
		}
		released = true
		return errors.Join(unlock(), owner.Close())
	}
	t.Cleanup(func() {
		if err := release(); err != nil {
			t.Errorf("release publisher ownership: %v", err)
		}
	})
	return owner, release
}

func writeOwnedSupervisionJournal(owner *os.File, data []byte) error {
	if err := owner.Truncate(0); err != nil {
		return err
	}
	if _, err := owner.Write(data); err != nil {
		return err
	}
	return owner.Sync()
}

func TestSupervisionGateWaitsForJournalPublication(t *testing.T) {
	store, opts := supervisionSnapshotFixture(t)
	supervisionAppend(t, store, opts, KindOpened, `{}`)
	supervisionAppend(t, store, opts, KindQuestion, `{}`)
	complete, err := os.ReadFile(store.Path(opts.Delivery))
	if err != nil {
		t.Fatal(err)
	}
	cut := bytes.IndexByte(complete, '\n') + 11
	if cut >= len(complete) {
		t.Fatal("two-record fixture has no partial second record")
	}
	owner, release := supervisionJournalOwner(t, store.Path(opts.Delivery))
	if err := writeOwnedSupervisionJournal(owner, complete[:cut]); err != nil {
		t.Errorf("publish partial record: %v", err)
		return
	}
	var early *supervisionGateResult
	var final supervisionGateResult
	var restoreErr error
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan supervisionGateResult, 1)
		go func() {
			gate, err := CheckSupervisionGate(ctx, opts)
			done <- supervisionGateResult{gate, err}
		}()
		synctest.Wait()
		select {
		case got := <-done:
			early = &got
		default:
		}
		_, restoreErr = owner.Write(complete[cut:])
		if restoreErr == nil {
			restoreErr = owner.Sync()
		}
		if restoreErr != nil {
			cancel()
		}
		restoreErr = errors.Join(restoreErr, release())
		if early != nil {
			final = *early
		} else {
			final = <-done
		}
	})
	if restoreErr != nil {
		t.Errorf("restore complete journal and release publisher: %v", restoreErr)
	}
	if early != nil {
		t.Errorf("gate returned during partial owned publication: %+v", *early)
	}
	if final.err != nil || final.gate.Delivery != opts.Delivery || final.gate.Required || !final.gate.Cleared {
		t.Errorf("gate after complete publication = %+v, %v", final.gate, final.err)
	}
	if got, err := os.ReadFile(store.Path(opts.Delivery)); err != nil || !bytes.Equal(got, complete) {
		t.Errorf("restored journal differs from exact complete bytes: %v", err)
	}
}

func TestSupervisionGateCancellationWhileJournalOwned(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(context.Context, SupervisionOptions) (SupervisionGate, error)
	}{
		{"check", CheckSupervisionGate},
		{"judge", func(ctx context.Context, opts SupervisionOptions) (SupervisionGate, error) {
			return JudgeSupervisionGate(ctx, opts, SupervisionJudgment{
				Delivery: opts.Delivery, ReviewID: "review-one", EvidenceDigest: "sha256:evidence",
				Decision: "accept", Rationale: "Reviewed the exact evidence.",
			})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, opts := supervisionSnapshotFixture(t)
			supervisionAppend(t, store, opts, KindOpened, `{}`)
			supervisionAppend(t, store, opts, KindQuestion, `{}`)
			complete, err := os.ReadFile(store.Path(opts.Delivery))
			if err != nil {
				t.Fatal(err)
			}
			cut := bytes.IndexByte(complete, '\n') + 11
			if cut >= len(complete) {
				t.Fatal("two-record fixture has no partial second record")
			}
			owner, release := supervisionJournalOwner(t, store.Path(opts.Delivery))
			if err := writeOwnedSupervisionJournal(owner, complete[:cut]); err != nil {
				t.Errorf("publish partial record: %v", err)
				return
			}
			cause := errors.New("operator cancelled gate observation")
			var early, cancelled *supervisionGateResult
			var final supervisionGateResult
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancelCause(context.Background())
				defer cancel(nil)
				done := make(chan supervisionGateResult, 1)
				go func() {
					gate, err := tc.call(ctx, opts)
					done <- supervisionGateResult{gate, err}
				}()
				synctest.Wait()
				select {
				case got := <-done:
					early = &got
				default:
				}
				cancel(cause)
				synctest.Wait()
				if early == nil {
					select {
					case got := <-done:
						cancelled = &got
					default:
					}
				}
				releaseErr := release()
				if releaseErr != nil {
					t.Errorf("release publisher: %v", releaseErr)
				}
				switch {
				case early != nil:
					final = *early
				case cancelled != nil:
					final = *cancelled
				default:
					final = <-done
				}
			})
			if early != nil {
				t.Errorf("%s returned while publisher owned partial journal: %+v", tc.name, *early)
			}
			if cancelled == nil {
				t.Errorf("%s did not return on cancellation while publisher owned journal", tc.name)
			}
			if !errors.Is(final.err, cause) || final.gate.Cleared {
				t.Errorf("%s cancellation = %+v, %v; want context cause and closed gate", tc.name, final.gate, final.err)
			}
		})
	}
}

func TestSupervisionGatePreCancelledLegacyOpening(t *testing.T) {
	t.Parallel()
	store, opts := supervisionSnapshotFixture(t)
	supervisionAppend(t, store, opts, KindOpened, `{}`)
	cause := errors.New("operator cancelled legacy observation")
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(cause)
	gate, err := CheckSupervisionGate(ctx, opts)
	if !errors.Is(err, cause) || gate.Cleared {
		t.Errorf("pre-cancelled legacy gate = %+v, %v; want context cause and closed gate", gate, err)
	}
}

func TestSupervisionGateSnapshotStates(t *testing.T) {
	for _, tc := range []struct {
		name       string
		opening    journal.Kind
		detail     string
		mutate     func([]byte) []byte
		zero       bool
		wantNeeded bool
		wantClear  bool
		wantError  bool
	}{
		{name: "legacy", opening: KindOpened, detail: `{}`, wantClear: true},
		{name: "supervision-pending", opening: KindOpened, detail: `{"supervision":true}`, wantNeeded: true},
		{name: "zero", zero: true, wantError: true},
		{name: "empty", opening: KindOpened, detail: `{}`, mutate: func([]byte) []byte { return nil }, wantError: true},
		{name: "missing-final-lf", opening: KindOpened, detail: `{}`, mutate: func(b []byte) []byte { return b[:len(b)-1] }, wantError: true},
		{name: "missing-opening", opening: KindQuestion, detail: `{}`, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, opts := supervisionSnapshotFixture(t)
			var snapshot journal.Snapshot
			if !tc.zero {
				supervisionAppend(t, store, opts, tc.opening, tc.detail)
				if tc.mutate != nil {
					data, err := os.ReadFile(store.Path(opts.Delivery))
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(store.Path(opts.Delivery), tc.mutate(data), 0600); err != nil {
						t.Fatal(err)
					}
				}
				snapshot = supervisionSnapshot(t, store, opts)
			}
			gate, err := CheckSupervisionGateSnapshot(context.Background(), opts, snapshot)
			if (err != nil) != tc.wantError || gate.Delivery != opts.Delivery || gate.Required != tc.wantNeeded || gate.Cleared != tc.wantClear {
				t.Errorf("snapshot gate = %+v, %v; want delivery %q, required %v, cleared %v, error %v", gate, err, opts.Delivery, tc.wantNeeded, tc.wantClear, tc.wantError)
			}
			if !tc.wantError {
				current, currentErr := CheckSupervisionGate(context.Background(), opts)
				if currentErr != nil || gate != current {
					t.Errorf("snapshot gate = %+v, %v; current gate = %+v, %v", gate, err, current, currentErr)
				}
			}
		})
	}
}

func TestSupervisionGateSnapshotPreCancelledLegacyOpening(t *testing.T) {
	t.Parallel()
	store, opts := supervisionSnapshotFixture(t)
	supervisionAppend(t, store, opts, KindOpened, `{}`)
	snapshot := supervisionSnapshot(t, store, opts)
	cause := errors.New("operator cancelled snapshot observation")
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(cause)
	gate, err := CheckSupervisionGateSnapshot(ctx, opts, snapshot)
	if !errors.Is(err, cause) || gate.Cleared {
		t.Errorf("pre-cancelled snapshot gate = %+v, %v; want context cause and closed gate", gate, err)
	}
}

func TestSupervisionGateSnapshotUsesCapturedJournalWithoutReacquiring(t *testing.T) {
	store, opts := supervisionSnapshotFixture(t)
	supervisionAppend(t, store, opts, KindOpened, `{}`)
	snapshot := supervisionSnapshot(t, store, opts)
	owner, release := supervisionJournalOwner(t, store.Path(opts.Delivery))
	if err := writeOwnedSupervisionJournal(owner, []byte("damaged\n")); err != nil {
		t.Errorf("change journal after capture: %v", err)
		return
	}
	var beforeRelease *supervisionGateResult
	var final supervisionGateResult
	synctest.Test(t, func(t *testing.T) {
		done := make(chan supervisionGateResult, 1)
		go func() {
			gate, err := CheckSupervisionGateSnapshot(context.Background(), opts, snapshot)
			done <- supervisionGateResult{gate, err}
		}()
		synctest.Wait()
		select {
		case got := <-done:
			beforeRelease = &got
		default:
		}
		if err := release(); err != nil {
			t.Errorf("release publisher: %v", err)
		}
		if beforeRelease != nil {
			final = *beforeRelease
		} else {
			final = <-done
		}
	})
	if beforeRelease == nil {
		t.Error("snapshot gate reacquired journal ownership")
	}
	if final.err != nil || final.gate.Delivery != opts.Delivery || final.gate.Required || !final.gate.Cleared {
		t.Errorf("captured legacy gate = %+v, %v", final.gate, final.err)
	}
}

func TestSupervisionJudgeReadsCurrentJournalAfterSnapshotCapture(t *testing.T) {
	store, opts := supervisionSnapshotFixture(t)
	supervisionAppend(t, store, opts, KindOpened, `{"supervision":true}`)
	supervisionAppend(t, store, opts, KindQuestion, `{}`)
	captured := supervisionSnapshot(t, store, opts)
	if !captured.Complete() || len(captured.Records()) != 2 {
		t.Fatal("expected complete two-record snapshot before judgment")
	}
	owner, release := supervisionJournalOwner(t, store.Path(opts.Delivery))
	if err := writeOwnedSupervisionJournal(owner, []byte("damaged\n")); err != nil {
		t.Errorf("change journal after capture: %v", err)
		return
	}
	judgment := SupervisionJudgment{
		Delivery: opts.Delivery, ReviewID: "review-one", EvidenceDigest: "sha256:evidence",
		Decision: "accept", Rationale: "Reviewed the exact evidence.",
	}
	var beforeRelease *supervisionGateResult
	var final supervisionGateResult
	synctest.Test(t, func(t *testing.T) {
		done := make(chan supervisionGateResult, 1)
		go func() {
			gate, err := JudgeSupervisionGate(context.Background(), opts, judgment)
			done <- supervisionGateResult{gate, err}
		}()
		synctest.Wait()
		select {
		case got := <-done:
			beforeRelease = &got
		default:
		}
		if err := release(); err != nil {
			t.Errorf("release publisher: %v", err)
		}
		if beforeRelease != nil {
			final = *beforeRelease
		} else {
			final = <-done
		}
	})
	if beforeRelease != nil {
		t.Errorf("Judge returned while current journal was owned: %+v", *beforeRelease)
	}
	if final.err == nil || final.gate.Cleared {
		t.Errorf("Judge used stale journal after snapshot capture: %+v, %v", final.gate, final.err)
	}
}
