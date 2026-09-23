package executor

import (
	"encoding/json"
	"errors"
)

// ReceiptLimit is the largest compact receipt accepted by dispatch consumers.
const ReceiptLimit = 4 << 10

type SubmissionState string

const (
	SubmissionUnknown      SubmissionState = "unknown"
	SubmissionNotSubmitted SubmissionState = "not_submitted"
	SubmissionSubmitted    SubmissionState = "submitted"
	SubmissionUncertain    SubmissionState = "uncertain"
)

type TransportOutcome string

const (
	TransportUnknown      TransportOutcome = "unknown"
	TransportNotStarted   TransportOutcome = "not_started"
	TransportCompleted    TransportOutcome = "completed"
	TransportFailed       TransportOutcome = "failed"
	TransportDisconnected TransportOutcome = "disconnected"
	TransportCanceled     TransportOutcome = "canceled"
)

type WorkerOutcome string

const (
	WorkerClaimUnknown   WorkerOutcome = "unknown"
	WorkerClaimedSuccess WorkerOutcome = "success"
	WorkerClaimedFailure WorkerOutcome = "failure"
)

// Submission records whether a worker prompt may have been accepted.
type Submission struct {
	State SubmissionState `json:"state"`
}

// Transport records the client/worker communication outcome. Failure is a
// stable failure class, not the worker's account of what happened.
type Transport struct {
	Outcome TransportOutcome `json:"outcome"`
	Failure string           `json:"failure,omitempty"`
}

// WorkerClaim is the worker's unverified account of its execution outcome.
type WorkerClaim struct {
	Outcome WorkerOutcome `json:"outcome"`
	Detail  string        `json:"detail,omitempty"`
}

// ArtifactReference points to the complete, caller-owned execution evidence.
type ArtifactReference struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256,omitempty"`
}

// Receipt is a compact account of execution facts. It intentionally carries
// no proof-verification verdict.
type Receipt struct {
	Submission   Submission         `json:"submission"`
	Transport    Transport          `json:"transport"`
	Worker       WorkerClaim        `json:"worker"`
	Usage        *Usage             `json:"usage,omitempty"`
	Effort       string             `json:"effort,omitempty"`
	Evidence     *ArtifactReference `json:"evidence,omitempty"`
	Overflow     bool               `json:"overflow,omitempty"`
	OmittedBytes int                `json:"omitted_bytes,omitempty"`
}

// MarshalReceipt returns a JSON receipt no larger than ReceiptLimit. When the
// worker detail does not fit, the compact receipt visibly marks the omission
// and retains the complete evidence reference. The caller must persist that
// evidence before requesting compaction.
func MarshalReceipt(receipt Receipt) ([]byte, error) {
	if receipt.Submission.State == "" {
		receipt.Submission.State = SubmissionUnknown
	}
	if receipt.Transport.Outcome == "" {
		receipt.Transport.Outcome = TransportUnknown
	}
	if receipt.Worker.Outcome == "" {
		receipt.Worker.Outcome = WorkerClaimUnknown
	}
	payload, err := json.Marshal(receipt)
	if err != nil {
		return nil, err
	}
	if len(payload) <= ReceiptLimit {
		return payload, nil
	}
	if receipt.Evidence == nil || receipt.Evidence.Path == "" {
		return nil, errors.New("executor: oversized receipt has no evidence reference")
	}

	omittedBytes := len(receipt.Worker.Detail)
	receipt.Worker.Detail = ""
	receipt.Overflow = true
	receipt.OmittedBytes = omittedBytes
	payload, err = json.Marshal(receipt)
	if err != nil {
		return nil, err
	}
	if len(payload) > ReceiptLimit {
		return nil, errors.New("executor: essential receipt state exceeds limit")
	}
	return payload, nil
}
