package loop

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSupervisionGateSnapshotBindsCapturedTipAndCurrentEvidence(t *testing.T) {
	opts, store, spec := supervisionGateFixture(t)
	job := gateReview(t, opts, spec, "SHIP", true)
	prior, err := CheckSupervisionGate(context.Background(), opts)
	if err != nil || !prior.Required || !prior.Cleared || prior.ReviewID != job.ID || prior.EvidenceDigest == "" {
		t.Fatalf("reported SHIP gate = %+v, %v", prior, err)
	}
	captured := supervisionSnapshot(t, store, opts)
	supervisionAppend(t, store, opts, KindPresenceTakenOver, `{}`)

	fresh, err := CheckSupervisionGate(context.Background(), opts)
	if err != nil || !fresh.Required || !fresh.Cleared || fresh.ReviewID != prior.ReviewID {
		t.Fatalf("fresh gate after journal advance = %+v, %v; want same cleared review as %+v", fresh, err, prior)
	}
	if fresh.EvidenceDigest == "" || fresh.EvidenceDigest == prior.EvidenceDigest {
		t.Fatalf("fresh digest %q must differ from captured-tip digest %q", fresh.EvidenceDigest, prior.EvidenceDigest)
	}
	got, err := CheckSupervisionGateSnapshot(context.Background(), opts, captured)
	if err != nil || got != prior {
		t.Fatalf("snapshot gate = %+v, %v; want captured-tip gate %+v", got, err, prior)
	}

	if err := os.WriteFile(filepath.Join(job.Artifacts, "review.md"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err = CheckSupervisionGateSnapshot(context.Background(), opts, captured)
	if err == nil || got.Cleared {
		t.Fatalf("snapshot gate cleared with corrupted report artifact: %+v, %v", got, err)
	}
}
