package journal

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRejectedFirstAppendLeavesDeliveryUnknown(t *testing.T) {
	for _, tc := range []struct {
		name   string
		record Record
		want   error
	}{
		{"detail", Record{Kind: "first", Detail: json.RawMessage(`"` + strings.Repeat("x", maxRecordBytes) + `"`)}, ErrInvalidRecord},
		{"graph", Record{Kind: "first", Graph: json.RawMessage(`"` + strings.Repeat("x", maxRecordBytes) + `"`)}, ErrInvalidRecord},
		{"escaped-task", Record{Kind: "first", TaskID: strings.Repeat(`\`, maxRecordBytes/2)}, ErrInvalidRecord},
		{"invalid-time", Record{Kind: "first", At: time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := openStore(t)
			if _, err := store.Append("rejected", tc.record); err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Fatalf("Append() error = %v, want rejection %v", err, tc.want)
			}
			if _, err := os.Stat(store.Path("rejected")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("rejected first append left a journal: %v", err)
			}
			if _, err := store.Read("rejected"); !errors.Is(err, ErrUnknownDelivery) {
				t.Errorf("Read() error = %v, want unknown delivery", err)
			}
			ids, err := store.List()
			if err != nil || len(ids) != 0 {
				t.Errorf("List() = %v, %v; want no delivery", ids, err)
			}
		})
	}
}

func TestAppendIgnoresSuppliedChainMetadata(t *testing.T) {
	t.Parallel()
	store := openStore(t)
	record, err := store.Append("metadata", Record{
		Kind: "first", Seq: 999999, Prev: strings.Repeat("x", maxRecordBytes), Digest: strings.Repeat("y", maxRecordBytes),
	})
	if err != nil || record.Seq != 1 || record.Prev != "" || !strings.HasPrefix(record.Digest, "sha256:") {
		t.Fatalf("Append() = %#v, %v; supplied chain metadata must be replaced", record, err)
	}
	if records, err := store.Read("metadata"); err != nil || len(records) != 1 || records[0].Digest != record.Digest {
		t.Fatalf("Read() = %#v, %v", records, err)
	}
}
