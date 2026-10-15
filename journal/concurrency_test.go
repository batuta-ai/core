package journal

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestReadWaitsForOwnedPublication(t *testing.T) {
	store := openStore(t)
	const delivery = "publication"
	first, err := store.Append(delivery, Record{Kind: "delivery_opened"})
	if err != nil {
		t.Fatal(err)
	}
	second := Record{
		Seq:    2,
		At:     time.Date(2026, 9, 6, 1, 0, 2, 0, time.UTC),
		Kind:   "dispatch_intent",
		TaskID: "task_1",
		Detail: json.RawMessage(`{"task_ids":["task_1"]}`),
		Prev:   first.Digest,
	}
	second.Digest = digestOf(second)
	line, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := os.OpenFile(store.Path(delivery), os.O_RDWR|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	defer func() {
		if !closed {
			if err := owner.Close(); err != nil {
				t.Errorf("close publisher: %v", err)
			}
		}
	}()
	unlock, err := lockPublication(owner)
	if err != nil {
		t.Fatal(err)
	}
	locked := true
	defer func() {
		if locked {
			if err := unlock(); err != nil {
				t.Errorf("unlock publisher: %v", err)
			}
		}
	}()
	if _, err := owner.Write(line[:len(line)-1]); err != nil {
		t.Fatal(err)
	}
	if err := owner.Sync(); err != nil {
		t.Fatal(err)
	}

	type readResult struct {
		records []Record
		err     error
	}
	synctest.Test(t, func(t *testing.T) {
		done := make(chan readResult, 1)
		go func() {
			records, err := store.Read(delivery)
			done <- readResult{records, err}
		}()
		synctest.Wait()
		var early *readResult
		select {
		case result := <-done:
			early = &result
		default:
		}
		_, writeErr := owner.Write(append(line[len(line)-1:], '\n'))
		if writeErr == nil {
			writeErr = owner.Sync()
		}
		unlockErr := unlock()
		locked = false
		var closeErr error
		if unlockErr != nil {
			closeErr = owner.Close()
			closed = true
		}
		result := readResult{}
		if early != nil {
			result = *early
		} else {
			result = <-done
		}
		if writeErr != nil || unlockErr != nil || closeErr != nil {
			t.Fatalf("complete publication: write/sync = %v, unlock = %v, close = %v", writeErr, unlockErr, closeErr)
		}
		if early != nil {
			t.Fatalf("Read completed during owned publication: records = %#v, error = %v", early.records, early.err)
		}
		if result.err != nil || !reflect.DeepEqual(result.records, []Record{first, second}) {
			t.Fatalf("Read after publication = %#v, %v; want %#v", result.records, result.err, []Record{first, second})
		}
	})
}

func TestReadRejectsStaticDamageWithoutPublisher(t *testing.T) {
	for _, tc := range []struct {
		name string
		file func(string, string) string
		want error
	}{
		{"malformed", func(_, _ string) string { return "not-json\n" }, ErrInvalidRecord},
		{"truncated", func(first, second string) string { return first + "\n" + second[:len(second)-1] }, ErrInvalidRecord},
		{"broken-chain", func(_, second string) string { return second + "\n" }, ErrChainBroken},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := openStore(t)
			for _, kind := range []Kind{"first", "second"} {
				if _, err := store.Append("static", Record{Kind: kind}); err != nil {
					t.Fatal(err)
				}
			}
			original, err := os.ReadFile(store.Path("static"))
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSuffix(string(original), "\n"), "\n")
			if err := os.WriteFile(store.Path("static"), []byte(tc.file(lines[0], lines[1])), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Read("static"); !errors.Is(err, tc.want) {
				t.Fatalf("Read() error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestAppendSamplesDefaultTimeBeforeJournalOwnership(t *testing.T) {
	t.Parallel()
	a := openStore(t)
	root := filepath.Dir(filepath.Dir(a.root))
	b, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	const delivery = "stale-chain"
	timestamp := time.Date(2026, 9, 6, 2, 0, 0, 0, time.UTC)
	var bRecord Record
	var bErr error
	a.now = func() time.Time {
		bRecord, bErr = b.Append(delivery, Record{Kind: "b", At: timestamp.Add(-time.Second)})
		return timestamp
	}
	aRecord, aErr := a.Append(delivery, Record{Kind: "a"})
	if bErr != nil || aErr != nil {
		t.Fatalf("Append() errors: B = %v, A = %v", bErr, aErr)
	}
	records, err := a.Read(delivery)
	if err != nil || !reflect.DeepEqual(records, []Record{bRecord, aRecord}) ||
		bRecord.Seq != 1 || aRecord.Seq != 2 || aRecord.Prev != bRecord.Digest {
		t.Fatalf("Read() = %#v, %v; B = %#v, A = %#v", records, err, bRecord, aRecord)
	}
}
