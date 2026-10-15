package journal

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestAppendRecordBudgetRoundTrip(t *testing.T) {
	for _, existing := range []bool{false, true} {
		for _, encodedBytes := range []int{maxRecordBytes - 1, maxRecordBytes} {
			name := map[bool]string{false: "first", true: "existing"}[existing]
			name += map[bool]string{false: "/fits", true: "/full"}[encodedBytes == maxRecordBytes]
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				store := openStore(t)
				record := Record{Kind: "budget", At: time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC), Seq: 1, Digest: "sha256:" + strings.Repeat("0", 64), Detail: json.RawMessage(`""`)}
				var before []byte
				if existing {
					first, err := store.Append("budget", Record{Kind: "first"})
					if err != nil {
						t.Fatal(err)
					}
					record.Seq, record.Prev = 2, first.Digest
					before, err = os.ReadFile(store.Path("budget"))
					if err != nil {
						t.Fatal(err)
					}
				}
				base, err := json.Marshal(record)
				if err != nil {
					t.Fatal(err)
				}
				record.Detail = json.RawMessage(`"` + strings.Repeat("x", encodedBytes-len(base)) + `"`)
				encoded, err := json.Marshal(record)
				if err != nil || len(encoded) != encodedBytes {
					t.Fatalf("boundary fixture bytes = %d, %v; want %d", len(encoded), err, encodedBytes)
				}
				_, err = store.Append("budget", record)
				if encodedBytes < maxRecordBytes {
					if err != nil {
						t.Fatal(err)
					}
					records, err := store.Read("budget")
					if err != nil || len(records) != record.Seq || !bytes.Equal(records[len(records)-1].Detail, record.Detail) {
						t.Fatalf("accepted boundary did not round-trip: records=%d, error=%v", len(records), err)
					}
					return
				}
				if !errors.Is(err, ErrInvalidRecord) {
					t.Errorf("full-budget append error = %v, want ErrInvalidRecord", err)
				}
				if !existing {
					if _, err := os.Stat(store.Path("budget")); !errors.Is(err, os.ErrNotExist) {
						t.Errorf("rejected first append created journal: %v", err)
					}
					return
				}
				after, err := os.ReadFile(store.Path("budget"))
				if err != nil || !bytes.Equal(before, after) {
					t.Errorf("rejected append changed existing bytes: %v", err)
				}
				if records, err := store.Read("budget"); err != nil || len(records) != 1 {
					t.Errorf("existing journal no longer readable: count=%d, error=%v", len(records), err)
				}
			})
		}
	}
}
