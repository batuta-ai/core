package review

type Decision string

const (
	Ship          Decision = "SHIP"
	FixBeforeShip Decision = "FIX_BEFORE_SHIP"
	Rework        Decision = "REWORK"
)

type Criterion struct {
	ID       string `json:"id"`
	Violated bool   `json:"violated"`
}

// Verdict uses accepted, unsuppressed findings and explicit criterion results.
// Criteria evaluation and finding acceptance belong to the caller.
func Verdict(findings []Finding, spec []Criterion) Decision {
	for _, criterion := range spec {
		if criterion.Violated {
			return Rework
		}
	}
	decision := Ship
	for _, finding := range findings {
		switch finding.Severity {
		case Blocker:
			return Rework
		case Major:
			decision = FixBeforeShip
		}
	}
	return decision
}
