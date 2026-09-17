package loop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/batuta-ai/core/journal"
)

const StateReviewBlocked = "review_blocked"

// SupervisionGate controls progression only. Clearing it grants no conductor,
// merge or publication acceptance.
type SupervisionGate struct {
	Required       bool   `json:"required"`
	Cleared        bool   `json:"cleared"`
	Delivery       string `json:"delivery"`
	ReviewID       string `json:"review_id,omitempty"`
	EvidenceDigest string `json:"evidence_digest,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

// SupervisionJudgment is an explicit operator decision about exact delivery and
// review evidence. A different decision requires new evidence.
type SupervisionJudgment struct {
	Delivery       string `json:"delivery"`
	ReviewID       string `json:"review_id"`
	EvidenceDigest string `json:"evidence_digest"`
	Decision       string `json:"decision"`
	Rationale      string `json:"rationale"`
}

// CheckSupervisionGate revalidates current evidence, including saved judgment.
// Deliveries opened without supervision retain their historical behavior.
func CheckSupervisionGate(ctx context.Context, opts SupervisionOptions) (SupervisionGate, error) {
	return supervisionGate(ctx, opts, nil)
}

// JudgeSupervisionGate never launches or retries review. Uncertain ownership or
// missing/corrupt evidence must be reconciled before judgment can be recorded.
func JudgeSupervisionGate(ctx context.Context, opts SupervisionOptions, judgment SupervisionJudgment) (SupervisionGate, error) {
	if judgment.Delivery != opts.Delivery || judgment.ReviewID == "" || judgment.EvidenceDigest == "" || strings.TrimSpace(judgment.Rationale) == "" || (judgment.Decision != "accept" && judgment.Decision != "reject") {
		return SupervisionGate{}, errors.New("loop: judgment requires exact delivery, review, evidence digest, accept/reject and nonempty rationale")
	}
	return supervisionGate(ctx, opts, &judgment)
}

func supervisionGate(ctx context.Context, opts SupervisionOptions, judgment *SupervisionJudgment) (SupervisionGate, error) {
	gate := SupervisionGate{Delivery: opts.Delivery}
	opts, err := normalizeSupervisionOptions(opts)
	if err != nil {
		return gate, err
	}
	records, err := readSupervisionGateRecords(opts)
	if err != nil {
		return gate, err
	}
	if len(records) == 0 || records[0].Kind != KindOpened {
		return gate, errors.New("loop: missing delivery opening identity")
	}
	var opened openedDetail
	if err := json.Unmarshal(records[0].Detail, &opened); err != nil {
		return gate, err
	}
	gate.Required = opened.Supervision
	if !gate.Required {
		if judgment != nil {
			return gate, errors.New("loop: delivery has no supervision progression gate")
		}
		gate.Cleared = true
		return gate, nil
	}
	gate.Reason = "implementation finalization or review is pending"
	candidate := supervisionReviewCandidateInWorkspace(opts.Workspace, opts.Delivery, records)
	if candidate == nil || candidate.ID == "" {
		if judgment != nil {
			return gate, errors.New("loop: finalized delivery identity is unavailable")
		}
		return gate, nil
	}
	gate.ReviewID = candidate.ID
	directory := supervisionReviewDirectory(opts, *candidate)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return gate, err
	}
	release, err := trySupervisionGateOwnership(filepath.Join(directory, "ownership"))
	if err != nil {
		return gate, err
	}
	defer release()
	return supervisionGateEvidence(ctx, opts, records, candidate, judgment)
}

// Use the review's existing ownership guard without waiting behind a live job.
func trySupervisionGateOwnership(path string) (func(), error) {
	file, err := os.OpenFile(path+".guard", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	locked, err := tryLockExclusive(file)
	if err != nil || !locked {
		return nil, errors.Join(errors.New("loop: review ownership prevents progression judgment"), err, file.Close())
	}
	return func() { unlockFile(file); _ = file.Close() }, nil
}

func supervisionGateNoOwner(opts SupervisionOptions) error {
	entries, err := os.ReadDir(filepath.Join(opts.Workspace, journal.Dir))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".lock") {
			continue
		}
		owner, _, err := inspectPresence(filepath.Join(opts.Workspace, journal.Dir, entry.Name()))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || owner != nil {
			return errors.Join(errors.New("loop: live or unresolved runner ownership prevents progression judgment"), err)
		}
	}
	return nil
}

func supervisionGateEvidence(ctx context.Context, opts SupervisionOptions, records []journal.Record, candidate *SupervisionReviewJob, judgment *SupervisionJudgment) (SupervisionGate, error) {
	gate := SupervisionGate{Required: true, Delivery: opts.Delivery, ReviewID: candidate.ID, Reason: "review evidence is unavailable or incomplete; explicit evidence-bound judgment is required"}
	if err := ctx.Err(); err != nil {
		return gate, err
	}
	if err := supervisionGateNoOwner(opts); err != nil {
		return gate, err
	}
	job, err := loadSupervisionReview(opts, candidate)
	if err != nil {
		return gate, err
	}
	if job.State != "reported" {
		gate.Reason = "review " + job.State + "; reconcile review evidence and ownership before judgment"
		if judgment != nil {
			return gate, errors.New("loop: judgment requires intact reported review evidence")
		}
		return gate, nil
	}
	if err := validateSupervisionReviewSnapshot(ctx, job); err != nil {
		return gate, err
	}
	if err := supervisionReviewArtifacts(job, false); err != nil {
		return gate, err
	}
	outcome := job.Outcome
	if err := classifySupervisionReview(job); err != nil {
		return gate, err
	}
	if job.Outcome != outcome {
		return gate, errors.New("loop: recorded review outcome changed")
	}
	directory := supervisionReviewDirectory(opts, *job)
	raw, err := readSupervisionFile(filepath.Join(directory, "job.json"), 1<<20)
	if err != nil {
		return gate, err
	}
	// Include the journal tip as well as the entire job, so even a valid new
	// terminal record or a changed attempt budget invalidates a saved decision.
	identity, _ := json.Marshal([]string{opts.Delivery, records[len(records)-1].Digest, string(raw)})
	gate.EvidenceDigest = fmt.Sprintf("sha256:%x", sha256.Sum256(identity))
	path := filepath.Join(directory, "progression.json")
	data, err := readSupervisionFile(path, 1<<20)
	var saved *SupervisionJudgment
	stale := false
	if err == nil {
		saved = &SupervisionJudgment{}
		if err := json.Unmarshal(data, saved); err != nil {
			return gate, err
		}
		digest, digestErr := hex.DecodeString(strings.TrimPrefix(saved.EvidenceDigest, "sha256:"))
		if saved.Delivery != opts.Delivery || saved.ReviewID != job.ID || !strings.HasPrefix(saved.EvidenceDigest, "sha256:") || digestErr != nil || len(digest) != sha256.Size || strings.TrimSpace(saved.Rationale) == "" || (saved.Decision != "accept" && saved.Decision != "reject") {
			return gate, errors.New("loop: progression judgment no longer matches current evidence")
		}
		if saved.EvidenceDigest != gate.EvidenceDigest {
			saved, stale = nil, true
			gate.Reason = "saved progression judgment is stale; explicitly re-judge the current evidence digest"
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return gate, err
	}
	if judgment != nil {
		if judgment.ReviewID != job.ID || judgment.EvidenceDigest != gate.EvidenceDigest {
			return gate, errors.New("loop: stale or mismatched progression judgment evidence")
		}
		if saved != nil && *saved != *judgment {
			return gate, errors.New("loop: conflicting progression judgment")
		}
		if saved == nil {
			if err := supervisionGateNoOwner(opts); err != nil {
				return gate, err
			}
			if err := writeSupervisionJSON(path, judgment); err != nil {
				return gate, err
			}
			saved = judgment
		}
	}
	if stale && saved == nil {
		return gate, nil
	}
	if saved != nil {
		gate.Cleared = saved.Decision == "accept"
		gate.Reason = "operator progression judgment: " + saved.Decision
	} else {
		gate.Cleared = job.Outcome == "SHIP"
		if gate.Cleared {
			gate.Reason = "complete SHIP evidence clears progression only"
		}
	}
	return gate, nil
}

func readSupervisionGateRecords(opts SupervisionOptions) ([]journal.Record, error) {
	data, err := readSupervisionFile(filepath.Join(opts.Workspace, journal.Dir, opts.Delivery+".jsonl"), 256<<20)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		return nil, errors.New("loop: incomplete delivery journal prevents progression")
	}
	return journal.Decode(bytes.NewReader(data))
}
