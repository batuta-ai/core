package journal

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
	"testing/synctest"
)

func openSnapshotFile(t *testing.T, path string) *os.File {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := file.Close(); err != nil {
			t.Errorf("close borrowed file: %v", err)
		}
	})
	return file
}

func TestReadSnapshotCapturesChainAndCompletion(t *testing.T) {
	for _, tc := range []struct {
		name     string
		contents func([]byte) []byte
		want     int
		complete bool
	}{
		{"complete", func(b []byte) []byte { return b }, 2, true},
		{"missing-final-lf", func(b []byte) []byte { return b[:len(b)-1] }, 2, false},
		{"empty", func([]byte) []byte { return nil }, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := openStore(t)
			const delivery = "snapshot-chain"
			first, err := store.Append(delivery, Record{Kind: "first", Detail: json.RawMessage(`{"nested":[1]}`)})
			if err != nil {
				t.Fatal(err)
			}
			second, err := store.Append(delivery, Record{Kind: "second", Graph: json.RawMessage(`{"tasks":[{"id":"one"}]}`)})
			if err != nil {
				t.Fatal(err)
			}
			original, err := os.ReadFile(store.Path(delivery))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(store.Path(delivery), tc.contents(original), 0o644); err != nil {
				t.Fatal(err)
			}
			file := openSnapshotFile(t, store.Path(delivery))
			if _, err := file.Seek(1, 0); err != nil {
				t.Fatal(err)
			}
			snapshot, err := ReadSnapshot(context.Background(), file)
			if err != nil {
				t.Fatalf("ReadSnapshot() error = %v", err)
			}
			want := []Record{first, second}[:tc.want]
			if got := snapshot.Records(); len(got) != tc.want || (tc.want > 0 && !reflect.DeepEqual(got, want)) {
				t.Errorf("Records() = %#v, want %#v", got, want)
			}
			if snapshot.Complete() != tc.complete {
				t.Errorf("Complete() = %v, want %v", snapshot.Complete(), tc.complete)
			}
			if offset, err := file.Seek(0, 1); err != nil || offset != 1 {
				t.Errorf("borrowed file offset = %d, %v; want 1", offset, err)
			}
			if _, err := file.Stat(); err != nil {
				t.Errorf("borrowed file was closed: %v", err)
			}
		})
	}
}

func TestSnapshotAccessorsOwnTheirPayloads(t *testing.T) {
	t.Parallel()
	store := openStore(t)
	const delivery = "snapshot-copy"
	want, err := store.Append(delivery, Record{
		Kind: "opened", Detail: json.RawMessage(`{"nested":[1]}`), Graph: json.RawMessage(`{"tasks":[1]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	file := openSnapshotFile(t, store.Path(delivery))
	snapshot, err := ReadSnapshot(context.Background(), file)
	if err != nil {
		t.Fatal(err)
	}
	first := snapshot.Records()
	if len(first) != 1 || len(first[0].Detail) == 0 || len(first[0].Graph) == 0 {
		t.Fatalf("Records() = %#v, want one record with payloads", first)
	}
	first[0].Kind = "changed"
	first[0].Detail[0] = 'x'
	first[0].Graph[0] = 'x'
	first[0] = Record{}
	if err := os.WriteFile(store.Path(delivery), []byte("damaged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := snapshot.Records(); !reflect.DeepEqual(got, []Record{want}) {
		t.Errorf("Records() after caller mutation = %#v, want %#v", got, []Record{want})
	}
	if !snapshot.Complete() {
		t.Error("Complete() changed after caller mutation")
	}
}

func TestSnapshotZeroValue(t *testing.T) {
	t.Parallel()
	var snapshot Snapshot
	if got := snapshot.Records(); len(got) != 0 {
		t.Errorf("zero Snapshot.Records() = %#v", got)
	}
	if snapshot.Complete() {
		t.Error("zero Snapshot.Complete() = true")
	}
}

func TestReadSnapshotRejectsDamageWithoutPrefix(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, Record, Record) []byte
		want   error
	}{
		{"bad-json", func(t *testing.T, first, _ Record) []byte {
			return append(snapshotLine(t, first), []byte("not-json\n")...)
		}, ErrInvalidRecord},
		{"truncated-suffix", func(t *testing.T, first, _ Record) []byte {
			return append(snapshotLine(t, first), []byte(`{"seq":`)...)
		}, ErrInvalidRecord},
		{"hash", func(t *testing.T, first, second Record) []byte {
			second.Digest = "sha256:wrong"
			return snapshotLines(t, first, second)
		}, ErrChainBroken},
		{"sequence", func(t *testing.T, first, second Record) []byte { second.Seq++; return snapshotLines(t, first, second) }, ErrChainBroken},
		{"previous", func(t *testing.T, first, second Record) []byte {
			second.Prev = "sha256:wrong"
			return snapshotLines(t, first, second)
		}, ErrChainBroken},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := openStore(t)
			const delivery = "snapshot-damage"
			first, err := store.Append(delivery, Record{Kind: "first"})
			if err != nil {
				t.Fatal(err)
			}
			second, err := store.Append(delivery, Record{Kind: "second"})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(store.Path(delivery), tc.mutate(t, first, second), 0o644); err != nil {
				t.Fatal(err)
			}
			snapshot, err := ReadSnapshot(context.Background(), openSnapshotFile(t, store.Path(delivery)))
			if !errors.Is(err, tc.want) || len(snapshot.Records()) != 0 || snapshot.Complete() {
				t.Errorf("ReadSnapshot() = %#v, %v; want %v and no data", snapshot, err, tc.want)
			}
		})
	}
}

func snapshotLine(t *testing.T, record Record) []byte {
	t.Helper()
	line, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return append(line, '\n')
}

func snapshotLines(t *testing.T, first, second Record) []byte {
	t.Helper()
	return append(snapshotLine(t, first), snapshotLine(t, second)...)
}

func TestReadSnapshotRejectsOverBudgetAndPipe(t *testing.T) {
	t.Parallel()
	store := openStore(t)
	path := store.Path("sparse")
	writable, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writable.Truncate(maxJournalBytes + 1); err != nil {
		writable.Close()
		t.Fatal(err)
	}
	if err := writable.Close(); err != nil {
		t.Fatal(err)
	}
	pipeReader, pipeWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pipeReader.Close(); pipeWriter.Close() })
	for _, tc := range []struct {
		name string
		file *os.File
	}{
		{"over-budget", openSnapshotFile(t, path)},
		{"pipe-with-open-writer", pipeReader},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot, err := ReadSnapshot(context.Background(), tc.file)
			if err == nil || len(snapshot.Records()) != 0 || snapshot.Complete() {
				t.Errorf("ReadSnapshot() = %#v, %v; want rejection with no data", snapshot, err)
			}
			if _, err := tc.file.Stat(); err != nil {
				t.Errorf("borrowed file was closed: %v", err)
			}
		})
	}
}

func TestReadSnapshotCancellationReleasesOwnership(t *testing.T) {
	store := openStore(t)
	const delivery = "snapshot-cancel"
	if _, err := store.Append(delivery, Record{Kind: "first"}); err != nil {
		t.Fatal(err)
	}
	file := openSnapshotFile(t, store.Path(delivery))
	preCancelled, cancel := context.WithCancel(context.Background())
	cancel()
	preSnapshot, preErr := ReadSnapshot(preCancelled, file)
	if !errors.Is(preErr, context.Canceled) || len(preSnapshot.Records()) != 0 || preSnapshot.Complete() {
		t.Errorf("pre-cancelled ReadSnapshot() = %#v, %v", preSnapshot, preErr)
	}
	owner, err := os.OpenFile(store.Path(delivery), os.O_RDWR|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	unlock, err := lockPublication(owner)
	if err != nil {
		t.Fatal(err)
	}
	locked := true
	defer func() {
		if locked {
			if err := unlock(); err != nil {
				t.Errorf("unlock owner: %v", err)
			}
		}
	}()
	type result struct {
		snapshot Snapshot
		err      error
	}
	var early, cancelled *result
	var final result
	ctx, cancelLive := context.WithCancel(context.Background())
	defer cancelLive()
	synctest.Test(t, func(t *testing.T) {
		done := make(chan result, 1)
		go func() {
			snapshot, err := ReadSnapshot(ctx, file)
			done <- result{snapshot, err}
		}()
		synctest.Wait()
		select {
		case got := <-done:
			early = &got
		default:
		}
		cancelLive()
		synctest.Wait()
		select {
		case got := <-done:
			cancelled = &got
		default:
		}
		unlockErr := unlock()
		locked = false
		if early != nil {
			final = *early
		} else if cancelled != nil {
			final = *cancelled
		} else {
			final = <-done
		}
		if unlockErr != nil {
			t.Errorf("unlock owner: %v", unlockErr)
		}
	})
	if early != nil {
		t.Errorf("ReadSnapshot returned while peer owned journal: %#v", *early)
	}
	if cancelled == nil {
		t.Error("ReadSnapshot did not return when context was cancelled")
	}
	if !errors.Is(final.err, context.Canceled) || len(final.snapshot.Records()) != 0 || final.snapshot.Complete() {
		t.Errorf("cancelled ReadSnapshot() = %#v, %v", final.snapshot, final.err)
	}
	if snapshot, err := ReadSnapshot(context.Background(), file); err != nil || len(snapshot.Records()) != 1 {
		t.Errorf("ReadSnapshot after release = %#v, %v", snapshot, err)
	}
	if record, err := store.Append(delivery, Record{Kind: "second"}); err != nil || record.Seq != 2 {
		t.Errorf("Append after release = %#v, %v", record, err)
	}
}
