package executor

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMarshalReceiptSeparatesExecutionFacts(t *testing.T) {
	t.Parallel()
	receipt := Receipt{
		Submission: Submission{State: SubmissionSubmitted},
		Transport:  Transport{Outcome: TransportDisconnected, Failure: "connection lost"},
		Worker:     WorkerClaim{Outcome: WorkerClaimedSuccess, Detail: "implemented the task"},
	}

	payload, err := MarshalReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"submission", "transport", "worker"} {
		if len(decoded[field]) == 0 {
			t.Fatalf("receipt missing %q: %s", field, payload)
		}
	}
	if strings.Contains(string(payload), "verified") {
		t.Fatalf("worker claim was presented as verification: %s", payload)
	}
}

func TestMarshalReceiptMakesMissingOutcomesExplicit(t *testing.T) {
	t.Parallel()
	payload, err := MarshalReceipt(Receipt{})
	if err != nil {
		t.Fatal(err)
	}
	var receipt Receipt
	if err := json.Unmarshal(payload, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Submission.State != SubmissionUnknown || receipt.Transport.Outcome != TransportUnknown || receipt.Worker.Outcome != WorkerClaimUnknown {
		t.Fatalf("missing outcomes are not explicit: %#v", receipt)
	}
}

func TestDispatchReceiptPreservesModelAndPartialUsageProvenance(t *testing.T) {
	t.Parallel()
	zero := int64(0)
	output := int64(21)
	report := DispatchReport{
		Executor:           "codex",
		Model:              "gpt-5.6-sol",
		Effort:             "medium",
		RequestedTransport: "acp",
		Backend:            "acp",
		ExitClass:          "completed",
		Receipt: Receipt{
			Submission: Submission{State: SubmissionSubmitted},
			Transport:  Transport{Outcome: TransportCompleted},
			Worker:     WorkerClaim{Outcome: WorkerClaimedSuccess},
			Usage: &Usage{
				CachedInputTokens: &zero,
				OutputTokens:      &output,
				Provenance:        "acp/session-prompt/usage (draft)",
			},
		},
	}

	payload, err := marshalDispatchReport(report)
	if err != nil {
		t.Fatal(err)
	}
	var fixture map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fixture); err != nil {
		t.Fatal(err)
	}
	if string(fixture["model"]) != `"gpt-5.6-sol"` || string(fixture["effort"]) != `"medium"` {
		t.Fatalf("model provenance changed: %s", payload)
	}
	var receipt struct {
		Usage map[string]json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(fixture["receipt"], &receipt); err != nil {
		t.Fatal(err)
	}
	if _, present := receipt.Usage["input_tokens"]; present {
		t.Fatalf("unknown input tokens were invented: %s", payload)
	}
	if string(receipt.Usage["cached_input_tokens"]) != "0" || string(receipt.Usage["output_tokens"]) != "21" {
		t.Fatalf("reported counters changed: %s", payload)
	}
	if string(receipt.Usage["provenance"]) != `"acp/session-prompt/usage (draft)"` {
		t.Fatalf("usage provenance changed: %s", payload)
	}
}

func TestMarshalReceiptBoundsOverflowAndKeepsEvidence(t *testing.T) {
	t.Parallel()
	reference := ArtifactReference{Path: "receipts/task-2/full.json", SHA256: strings.Repeat("a", 64)}
	receipt := Receipt{
		Submission: Submission{State: SubmissionUncertain},
		Transport:  Transport{Outcome: TransportFailed, Failure: "protocol_error"},
		Worker:     WorkerClaim{Outcome: WorkerClaimUnknown, Detail: strings.Repeat("failure detail ", ReceiptLimit)},
		Evidence:   &reference,
	}

	payload, err := MarshalReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) > ReceiptLimit {
		t.Fatalf("receipt size = %d, limit = %d", len(payload), ReceiptLimit)
	}
	var compact Receipt
	if err := json.Unmarshal(payload, &compact); err != nil {
		t.Fatal(err)
	}
	if !compact.Overflow || compact.OmittedBytes == 0 {
		t.Fatalf("overflow is not visible: %#v", compact)
	}
	if compact.Evidence == nil || *compact.Evidence != reference {
		t.Fatalf("evidence reference changed: %#v", compact.Evidence)
	}
	if compact.Submission.State != SubmissionUncertain || compact.Transport.Outcome != TransportFailed || compact.Transport.Failure != "protocol_error" || compact.Worker.Outcome != WorkerClaimUnknown {
		t.Fatalf("essential failure state changed: %#v", compact)
	}
	if compact.Worker.Detail != "" {
		t.Fatalf("overflow detail retained: %d bytes", len(compact.Worker.Detail))
	}
}

func TestMarshalReceiptRefusesUnreferencedOverflow(t *testing.T) {
	t.Parallel()
	_, err := MarshalReceipt(Receipt{Worker: WorkerClaim{Detail: strings.Repeat("x", ReceiptLimit+1)}})
	if err == nil {
		t.Fatal("MarshalReceipt() accepted overflow without an evidence reference")
	}
}
