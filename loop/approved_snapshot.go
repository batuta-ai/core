package loop

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/routing"
)

var (
	ErrSnapshotInvalid  = errors.New("loop: invalid approved snapshot")
	ErrSnapshotRequired = errors.New("loop: delivery requires its approved snapshot")
	ErrSnapshotMismatch = errors.New("loop: approved snapshot differs from delivery opening")
	ErrDeliveryExists   = errors.New("loop: delivery already opened; use Resume")
)

// ApprovedPlanSnapshot is an explicit caller attestation over exact plan bytes.
// ReceiptID is opaque; the caller owns authorization and durable receipt storage.
type ApprovedPlanSnapshot struct {
	Slug          string
	Path          string
	ReceiptID     string
	ContentDigest string
	Content       []byte
}

func ownApprovedSnapshot(snapshot *ApprovedPlanSnapshot) (*ApprovedPlanSnapshot, error) {
	if snapshot == nil {
		return nil, nil
	}
	if !journal.ValidDeliveryID(snapshot.Slug) || snapshot.ReceiptID == "" || !utf8.ValidString(snapshot.ReceiptID) ||
		strings.TrimSpace(snapshot.ReceiptID) != snapshot.ReceiptID || len(snapshot.ReceiptID) > 1024 || len(snapshot.Content) == 0 || len(snapshot.Content) > 1<<20 {
		return nil, ErrSnapshotInvalid
	}
	validPath := false
	for _, location := range routing.PlanLocations(snapshot.Slug) {
		validPath = validPath || snapshot.Path == filepath.ToSlash(location)
	}
	if !validPath {
		return nil, ErrSnapshotInvalid
	}
	owned := *snapshot
	owned.Content = append([]byte(nil), snapshot.Content...)
	digest := sha256.Sum256(owned.Content)
	if owned.ContentDigest != hex.EncodeToString(digest[:]) {
		return nil, ErrSnapshotInvalid
	}
	return &owned, nil
}

func (r *Runner) loadApprovedSnapshot(reference string) error {
	snapshot := r.opts.ApprovedSnapshot
	if reference != "" && reference != snapshot.Slug && reference != snapshot.Path && reference != filepath.FromSlash(snapshot.Path) {
		return ErrSnapshotInvalid
	}
	plan, err := routing.ParsePlan(snapshot.Slug, snapshot.Content)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrSnapshotInvalid, err)
	}
	if plan.Status != routing.PlanProposed && plan.Status != routing.PlanApproved {
		return ErrSnapshotInvalid
	}
	// Approval is the caller's explicit attestation, never a source-header write.
	plan.Status, plan.Path = routing.PlanApproved, snapshot.Path
	r.plan, r.planPath = plan, filepath.Join(r.root, filepath.FromSlash(snapshot.Path))
	return nil
}

func (r *Runner) validateSnapshotOpening(opened openedDetail) error {
	snapshot := r.opts.ApprovedSnapshot
	if opened.ApprovalReceiptID == "" && opened.PlanContentDigest == "" {
		if snapshot != nil {
			return ErrSnapshotMismatch
		}
		return nil
	}
	if snapshot == nil {
		return ErrSnapshotRequired
	}
	if opened.ApprovalReceiptID != snapshot.ReceiptID || opened.PlanContentDigest != snapshot.ContentDigest || opened.Slug != snapshot.Slug || opened.PlanPath != snapshot.Path || opened.Workspace != r.root {
		return ErrSnapshotMismatch
	}
	if err := r.loadApprovedSnapshot(r.opts.Plan); err != nil {
		return err
	}
	if r.plan.Set.Digest != opened.PlanDigest {
		return ErrSnapshotMismatch
	}
	return nil
}

func approvedSnapshotPath(snapshot *ApprovedPlanSnapshot) string {
	if snapshot == nil {
		return ""
	}
	return snapshot.Path
}

func (r *Runner) foreignSnapshotEntries(entries []string) []string {
	if r.opts.ApprovedSnapshot == nil {
		return entries
	}
	foreign := make([]string, 0, len(entries))
	for _, entry := range entries {
		if len(entry) > 3 && entry[3:] == r.opts.ApprovedSnapshot.Path && (entry[:3] == " M " || entry[:3] == " D " || entry[:3] == "?? ") {
			continue
		}
		foreign = append(foreign, entry)
	}
	return foreign
}
