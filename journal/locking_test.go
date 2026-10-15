package journal

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
)

func TestAppendWaitsForJournalOwnership(t *testing.T) {
	a := openStore(t)
	root := filepath.Dir(filepath.Dir(a.root))
	b, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	const delivery = "append-ownership"
	first, err := a.Append(delivery, Record{Kind: "first"})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := os.OpenFile(a.Path(delivery), os.O_RDWR|os.O_APPEND, 0)
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

	type appendResult struct {
		record Record
		err    error
	}
	synctest.Test(t, func(t *testing.T) {
		done := make(chan appendResult, 1)
		go func() {
			record, err := b.Append(delivery, Record{Kind: "second"})
			done <- appendResult{record, err}
		}()
		synctest.Wait()
		var early *appendResult
		select {
		case result := <-done:
			early = &result
		default:
		}
		unlockErr := unlock()
		locked = false
		if unlockErr != nil {
			if err := owner.Close(); err != nil {
				t.Errorf("close owner after failed unlock: %v", err)
			}
		}
		result := appendResult{}
		if early != nil {
			result = *early
		} else {
			result = <-done
		}
		if unlockErr != nil {
			t.Fatalf("unlock owner: %v", unlockErr)
		}
		if early != nil {
			t.Fatalf("Append completed during owned transaction: record = %#v, error = %v", early.record, early.err)
		}
		if result.err != nil || result.record.Seq != 2 || result.record.Prev != first.Digest {
			t.Fatalf("Append after ownership = %#v, %v; first = %#v", result.record, result.err, first)
		}
		records, err := a.Read(delivery)
		if err != nil || !reflect.DeepEqual(records, []Record{first, result.record}) {
			t.Fatalf("Read() = %#v, %v; want %#v", records, err, []Record{first, result.record})
		}
	})
}

func TestJournalOwnershipReleasesAfterValidationError(t *testing.T) {
	for _, operation := range []string{"read", "append"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			store := openStore(t)
			const delivery = "damaged"
			if err := os.WriteFile(store.Path(delivery), []byte("invalid-json\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			var err error
			if operation == "read" {
				_, err = store.Read(delivery)
			} else {
				_, err = store.Append(delivery, Record{Kind: "next"})
			}
			if !errors.Is(err, ErrInvalidRecord) {
				t.Fatalf("%s error = %v, want invalid record", operation, err)
			}
			owner, err := os.OpenFile(store.Path(delivery), os.O_RDWR, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer owner.Close()
			unlock, err := lockPublication(owner)
			if err != nil {
				t.Fatalf("ownership remains held after %s: %v", operation, err)
			}
			if err := unlock(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReadAcceptsCompleteRecordWithoutFinalNewline(t *testing.T) {
	t.Parallel()
	store := openStore(t)
	const delivery = "no-final-newline"
	want, err := store.Append(delivery, Record{Kind: "first"})
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.ReadFile(store.Path(delivery))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.Path(delivery), []byte(strings.TrimSuffix(string(file), "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	records, err := store.Read(delivery)
	if err != nil || !reflect.DeepEqual(records, []Record{want}) {
		t.Fatalf("Read() = %#v, %v; want %#v", records, err, []Record{want})
	}
}
