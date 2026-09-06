// Package journal is the durable memory of a `batuta loop` run on a file
// host: one append-only JSONL file per delivery under `.batuta/journal/`.
//
// Every record carries the delivery graph as it stood after the recorded
// transition, so `--resume` never re-derives state from partial evidence:
// it loads the last record, validates the hash chain and continues from
// there. The graph snapshot is small (at most 64 tasks) and the chain
// (`prev` → `digest`) makes a truncated or edited file detectable.
//
// Authority: on file hosts this journal is the single authority for a
// delivery. The routing ownership store (`routing/ownership.go`) stays the
// daemon's; the loop never writes it.
package journal

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	// Dir is the journal directory relative to the workspace root.
	Dir = ".batuta/journal"

	maxRecordBytes  = 4 << 20
	maxJournalBytes = 256 << 20
)

var (
	ErrInvalidRecord   = errors.New("journal: invalid record")
	ErrChainBroken     = errors.New("journal: hash chain is broken")
	ErrUnknownDelivery = errors.New("journal: unknown delivery")

	deliveryIDPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
)

// Kind names what a record witnessed. The loop package owns the vocabulary;
// the journal only requires a non-empty, bounded kind.
type Kind string

// Record is one journal line. Seq, At, Prev and Digest are filled by
// Append; callers set Kind, TaskID, Detail and Graph.
type Record struct {
	Seq    int             `json:"seq"`
	At     time.Time       `json:"at"`
	Kind   Kind            `json:"kind"`
	TaskID string          `json:"task_id,omitempty"`
	Detail json.RawMessage `json:"detail,omitempty"`
	Graph  json.RawMessage `json:"graph,omitempty"`
	Prev   string          `json:"prev"`
	Digest string          `json:"digest"`
}

// Store reads and appends delivery journals under one workspace.
type Store struct {
	root string
	now  func() time.Time
}

// Open prepares the journal directory under the workspace root. The root
// must be absolute; the directory is created on first use.
func Open(workspaceRoot string) (*Store, error) {
	if strings.TrimSpace(workspaceRoot) == "" || !filepath.IsAbs(workspaceRoot) {
		return nil, errors.New("journal: workspace root must be absolute")
	}
	root := filepath.Join(filepath.Clean(workspaceRoot), filepath.FromSlash(Dir))
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("journal: create %s: %w", Dir, err)
	}
	return &Store{root: root, now: func() time.Time { return time.Now().UTC() }}, nil
}

// Path is the journal file of a delivery.
func (s *Store) Path(deliveryID string) string {
	return filepath.Join(s.root, deliveryID+".jsonl")
}

// ValidDeliveryID says whether a delivery identifier is a plain slug.
func ValidDeliveryID(id string) bool {
	return len(id) <= 128 && deliveryIDPattern.MatchString(id)
}

// Append writes one record at the end of the delivery's journal and returns
// it with Seq, At, Prev and Digest filled. The file is opened in append mode
// and synced before returning, so a crash right after Append leaves a
// complete line behind.
func (s *Store) Append(deliveryID string, record Record) (Record, error) {
	if !ValidDeliveryID(deliveryID) {
		return Record{}, ErrUnknownDelivery
	}
	if strings.TrimSpace(string(record.Kind)) == "" || len(record.Kind) > 64 ||
		(record.Detail != nil && !json.Valid(record.Detail)) || (record.Graph != nil && !json.Valid(record.Graph)) {
		return Record{}, ErrInvalidRecord
	}
	previous, err := s.Read(deliveryID)
	if err != nil && !errors.Is(err, ErrUnknownDelivery) {
		return Record{}, err
	}
	record.Seq = len(previous) + 1
	record.Prev = ""
	if len(previous) > 0 {
		record.Prev = previous[len(previous)-1].Digest
	}
	if record.At.IsZero() {
		record.At = s.now()
	}
	record.At = record.At.UTC()
	record.Digest = ""
	record.Digest = digestOf(record)
	line, err := json.Marshal(record)
	if err != nil {
		return Record{}, fmt.Errorf("journal: encode record: %w", err)
	}
	if len(line) > maxRecordBytes {
		return Record{}, ErrInvalidRecord
	}
	handle, err := os.OpenFile(s.Path(deliveryID), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return Record{}, fmt.Errorf("journal: open: %w", err)
	}
	defer handle.Close()
	if _, err := handle.Write(append(line, '\n')); err != nil {
		return Record{}, fmt.Errorf("journal: write: %w", err)
	}
	if err := handle.Sync(); err != nil {
		return Record{}, fmt.Errorf("journal: sync: %w", err)
	}
	return record, nil
}

// Read returns every record of a delivery after checking the chain. A
// missing file is ErrUnknownDelivery; a broken chain or an unparsable line
// is an error, never a partial result.
func (s *Store) Read(deliveryID string) ([]Record, error) {
	if !ValidDeliveryID(deliveryID) {
		return nil, ErrUnknownDelivery
	}
	handle, err := os.Open(s.Path(deliveryID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrUnknownDelivery
		}
		return nil, fmt.Errorf("journal: open: %w", err)
	}
	defer handle.Close()
	info, err := handle.Stat()
	if err != nil || info.Size() > maxJournalBytes {
		return nil, errors.New("journal: file is unreadable or over budget")
	}
	return Decode(handle)
}

// Decode parses a journal stream and verifies its chain.
func Decode(reader io.Reader) ([]Record, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), maxRecordBytes)
	records := make([]Record, 0, 16)
	prev := ""
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var record Record
		if err := json.Unmarshal(line, &record); err != nil {
			return nil, fmt.Errorf("%w: line %d", ErrInvalidRecord, len(records)+1)
		}
		if record.Seq != len(records)+1 || record.Prev != prev || record.Digest == "" {
			return nil, fmt.Errorf("%w: record %d", ErrChainBroken, len(records)+1)
		}
		expected := record.Digest
		record.Digest = ""
		if digestOf(record) != expected {
			return nil, fmt.Errorf("%w: record %d digest", ErrChainBroken, record.Seq)
		}
		record.Digest = expected
		prev = expected
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("journal: read: %w", err)
	}
	return records, nil
}

// Last returns the final record of a delivery.
func (s *Store) Last(deliveryID string) (Record, error) {
	records, err := s.Read(deliveryID)
	if err != nil {
		return Record{}, err
	}
	if len(records) == 0 {
		return Record{}, ErrUnknownDelivery
	}
	return records[len(records)-1], nil
}

// List returns the delivery identifiers with a journal, most recent first.
func (s *Store) List() ([]string, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, fmt.Errorf("journal: list: %w", err)
	}
	type dated struct {
		id string
		at time.Time
	}
	found := make([]dated, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		id := strings.TrimSuffix(name, ".jsonl")
		if !ValidDeliveryID(id) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		found = append(found, dated{id: id, at: info.ModTime()})
	}
	sort.Slice(found, func(i, j int) bool {
		if found[i].at.Equal(found[j].at) {
			return found[i].id < found[j].id
		}
		return found[i].at.After(found[j].at)
	})
	ids := make([]string, 0, len(found))
	for _, entry := range found {
		ids = append(ids, entry.id)
	}
	return ids, nil
}

func digestOf(record Record) string {
	payload, err := json.Marshal(struct {
		Seq    int             `json:"seq"`
		At     time.Time       `json:"at"`
		Kind   Kind            `json:"kind"`
		TaskID string          `json:"task_id,omitempty"`
		Detail json.RawMessage `json:"detail,omitempty"`
		Graph  json.RawMessage `json:"graph,omitempty"`
		Prev   string          `json:"prev"`
	}{record.Seq, record.At.UTC(), record.Kind, record.TaskID, compactJSON(record.Detail), compactJSON(record.Graph), record.Prev})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// compactJSON normalizes raw JSON so the digest does not depend on the
// whitespace a decoder round-trip may or may not preserve.
func compactJSON(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return raw
	}
	compact, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return compact
}
