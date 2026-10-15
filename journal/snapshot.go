package journal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
)

// Snapshot is one validated observation of a borrowed journal descriptor.
type Snapshot struct {
	records  []Record
	complete bool
}

// Records returns the records captured in the snapshot.
func (s Snapshot) Records() []Record {
	if len(s.records) == 0 {
		return nil
	}
	records := make([]Record, len(s.records))
	for i, record := range s.records {
		record.Detail = bytes.Clone(record.Detail)
		record.Graph = bytes.Clone(record.Graph)
		records[i] = record
	}
	return records
}

// Complete reports whether captured nonempty bytes ended in a newline.
func (s Snapshot) Complete() bool { return s.complete }

// ReadSnapshot validates one bounded observation of a borrowed regular file.
// The caller must bind the read-only descriptor to the intended path, borrow it
// exclusively for this call, and close it afterward, including on error. The
// snapshot verifies the descriptor and journal chain, not their provenance or
// authenticity. Raw or older participants and file replacement do not share
// its publication guarantee.
func ReadSnapshot(ctx context.Context, file *os.File) (snapshot Snapshot, resultErr error) {
	if ctx == nil {
		return Snapshot{}, errors.New("journal: nil context")
	}
	if err := context.Cause(ctx); err != nil {
		return Snapshot{}, err
	}
	if file == nil {
		return Snapshot{}, errors.New("journal: nil descriptor")
	}
	info, err := file.Stat()
	if err != nil {
		return Snapshot{}, fmt.Errorf("journal: stat: %w", err)
	}
	if !info.Mode().IsRegular() {
		return Snapshot{}, errors.New("journal: file is unreadable or over budget")
	}
	unlock, err := lockJournalContext(ctx, file)
	if err != nil {
		return Snapshot{}, fmt.Errorf("journal: lock: %w", err)
	}
	defer func() {
		resultErr = errors.Join(resultErr, unlock())
		if resultErr != nil {
			snapshot = Snapshot{}
		}
	}()
	info, err = file.Stat()
	if err != nil {
		return Snapshot{}, fmt.Errorf("journal: stat: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maxJournalBytes {
		return Snapshot{}, errors.New("journal: file is unreadable or over budget")
	}
	data := make([]byte, int(info.Size()))
	if _, err := io.ReadFull(io.NewSectionReader(file, 0, info.Size()), data); err != nil {
		return Snapshot{}, fmt.Errorf("journal: read: %w", err)
	}
	records, err := Decode(bytes.NewReader(data))
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{records: records, complete: len(data) > 0 && data[len(data)-1] == '\n'}, nil
}
