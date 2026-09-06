package loop

import (
	"strings"
	"testing"

	"github.com/batuta-ai/core/gates"
)

func TestBriefCarriesTheProgressProtocol(t *testing.T) {
	t.Parallel()
	brief := Brief(BriefInput{Criteria: []gates.Criterion{
		{Text: "first criterion", Proof: "test -f first"},
		{Text: "second criterion", Proof: "test -f second"},
	}})
	_, section, found := strings.Cut(brief, "## Progress protocol\n\n")
	if !found {
		t.Fatalf("brief has no progress protocol section:\n%s", brief)
	}
	section, _, _ = strings.Cut(section, "\n## ")
	for _, want := range []string{
		"BATUTA-PROGRESS <n> START", "before the first edit toward it",
		"BATUTA-PROGRESS <n> DONE", "when its proof passes locally",
		"plain text on stdout", "nothing else on that line", "no tool required",
		"1-based positions", "## Acceptance criteria", "BATUTA-QUESTION:",
	} {
		if !strings.Contains(section, want) {
			t.Errorf("progress protocol lacks %q:\n%s", want, section)
		}
	}
}
