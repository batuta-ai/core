package loop

import (
	"context"
	"errors"

	"github.com/batuta-ai/core/journal"
)

// CheckSupervisionGateSnapshot checks a caller-bound, coherent journal observation.
// The caller owns the source binding and supplies a fresh snapshot for the delivery.
func CheckSupervisionGateSnapshot(ctx context.Context, opts SupervisionOptions, snapshot journal.Snapshot) (SupervisionGate, error) {
	gate := SupervisionGate{Delivery: opts.Delivery}
	opts, err := normalizeSupervisionOptions(opts)
	if err != nil {
		return gate, err
	}
	if ctx == nil {
		return gate, errors.New("loop: nil context")
	}
	if err := context.Cause(ctx); err != nil {
		return gate, err
	}
	records, err := supervisionGateSnapshotRecords(snapshot)
	if err != nil {
		return gate, err
	}
	return supervisionGateWithRecords(ctx, opts, records, nil)
}
