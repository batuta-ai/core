package journal

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openStore(t *testing.T) *Store {
	t.Helper()
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	store, err := Open(root)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	tick := time.Date(2026, 9, 6, 1, 0, 0, 0, time.UTC)
	store.now = func() time.Time { tick = tick.Add(time.Second); return tick }
	return store
}

func TestAppendBuildsAChainAndReadVerifiesIt(t *testing.T) {
	store := openStore(t)
	first, err := store.Append("checkout-a1b2", Record{Kind: "delivery_opened", Detail: json.RawMessage(`{"slug":"checkout"}`)})
	if err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	second, err := store.Append("checkout-a1b2", Record{Kind: "wave_admitted", TaskID: "task_1", Graph: json.RawMessage(`{ "tasks": [] }`)})
	if err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if first.Seq != 1 || first.Prev != "" || !strings.HasPrefix(first.Digest, "sha256:") {
		t.Fatalf("first = %#v", first)
	}
	if second.Seq != 2 || second.Prev != first.Digest || second.At.Before(first.At) {
		t.Fatalf("second = %#v", second)
	}
	records, err := store.Read("checkout-a1b2")
	if err != nil || len(records) != 2 || records[1].TaskID != "task_1" || string(records[1].Graph) != `{"tasks":[]}` {
		t.Fatalf("Read() = %#v, %v", records, err)
	}
	last, err := store.Last("checkout-a1b2")
	if err != nil || last.Seq != 2 {
		t.Fatalf("Last() = %#v, %v", last, err)
	}
	ids, err := store.List()
	if err != nil || len(ids) != 1 || ids[0] != "checkout-a1b2" {
		t.Fatalf("List() = %v, %v", ids, err)
	}
}

func TestReadRejectsATamperedOrTruncatedChain(t *testing.T) {
	store := openStore(t)
	for _, kind := range []Kind{"a", "b", "c"} {
		if _, err := store.Append("d", Record{Kind: kind, Detail: json.RawMessage(`{"n":1}`)}); err != nil {
			t.Fatalf("Append(%s) error = %v", kind, err)
		}
	}
	path := store.Path("d")
	original, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimRight(string(original), "\n"), "\n")

	// Editing a detail without re-signing breaks the digest of that record.
	tampered := strings.Replace(lines[1], `{"n":1}`, `{"n":2}`, 1)
	os.WriteFile(path, []byte(strings.Join([]string{lines[0], tampered, lines[2]}, "\n")+"\n"), 0o644)
	if _, err := store.Read("d"); !errors.Is(err, ErrChainBroken) {
		t.Fatalf("Read(tampered) error = %v, want chain broken", err)
	}
	// Dropping a middle record breaks the prev link.
	os.WriteFile(path, []byte(strings.Join([]string{lines[0], lines[2]}, "\n")+"\n"), 0o644)
	if _, err := store.Read("d"); !errors.Is(err, ErrChainBroken) {
		t.Fatalf("Read(gap) error = %v, want chain broken", err)
	}
	// A truncated trailing line (crash mid-write) is invalid, never silently dropped.
	os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"+lines[2][:20]), 0o644)
	if _, err := store.Read("d"); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("Read(truncated) error = %v, want invalid record", err)
	}
	// Restored, it reads again.
	os.WriteFile(path, original, 0o644)
	if records, err := store.Read("d"); err != nil || len(records) != 3 {
		t.Fatalf("Read(restored) = %d, %v", len(records), err)
	}
}

func TestAppendValidatesItsInput(t *testing.T) {
	store := openStore(t)
	if _, err := store.Append("Bad_ID", Record{Kind: "x"}); !errors.Is(err, ErrUnknownDelivery) {
		t.Fatalf("bad id error = %v", err)
	}
	if _, err := store.Append("ok", Record{}); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("empty kind error = %v", err)
	}
	if _, err := store.Append("ok", Record{Kind: "x", Detail: json.RawMessage(`{not json`)}); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("bad detail error = %v", err)
	}
	if _, err := store.Read("missing"); !errors.Is(err, ErrUnknownDelivery) {
		t.Fatalf("missing error = %v", err)
	}
	if _, err := Open("relative/path"); err == nil {
		t.Fatal("Open(relative) should fail")
	}
}
