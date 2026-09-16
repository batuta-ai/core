package executor

import (
	"encoding/json"
	"testing"
)

func TestUsageMissingCountersRemainUnknown(t *testing.T) {
	t.Parallel()
	usage := Usage{Provenance: "acp/session-update"}
	payload, err := json.Marshal(usage)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != `{"provenance":"acp/session-update"}` {
		t.Fatalf("json.Marshal() = %s", payload)
	}
	if total, known := usage.TotalTokens(); known || total != 0 {
		t.Fatalf("TotalTokens() = %d, %t; want unknown", total, known)
	}
}

func TestUsageTotalDoesNotDoubleCountCachedInput(t *testing.T) {
	t.Parallel()
	input, cached, output := int64(100), int64(40), int64(25)
	usage := Usage{
		InputTokens:       &input,
		CachedInputTokens: &cached,
		OutputTokens:      &output,
		Provenance:        "worker/final-result",
	}
	if total, known := usage.TotalTokens(); !known || total != 125 {
		t.Fatalf("TotalTokens() = %d, %t; want 125, true", total, known)
	}
	if usage.Provenance != "worker/final-result" {
		t.Fatalf("provenance changed: %q", usage.Provenance)
	}
}

func TestUsageTotalRequiresInputAndOutput(t *testing.T) {
	t.Parallel()
	input, cached, output := int64(10), int64(7), int64(3)
	for _, usage := range []Usage{
		{InputTokens: &input},
		{OutputTokens: &output},
		{CachedInputTokens: &cached, OutputTokens: &output},
	} {
		if total, known := usage.TotalTokens(); known || total != 0 {
			t.Fatalf("TotalTokens() = %d, %t for %#v; want unknown", total, known, usage)
		}
	}
}
