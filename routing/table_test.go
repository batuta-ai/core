package routing

import (
	"errors"
	"strings"
	"testing"

	"github.com/batuta-ai/core/inventory"
)

const routingTableFixture = `# Routing — checkout
<!-- inputs: profile.md@sha256:1a2b3c4d5e6f -->

Prose the conductor reads.

| Lane | Domain | Executor | Model | Cost |
|---|---|---|---|---|
| low | * | opencode | ` + "`kimi/k2.5`" + ` | cents |
| low | frontend | cursor-agent | composer-2.5 | subscription |
| medium | * | codex | gpt-5.6-terra | subscription |
| high | * | codex | gpt-5.6-sol | subscription |
| critical | * | self | — | host |

| Role | Executor |
|---|---|
| research | opencode |
`

func tableSnapshot(t *testing.T, available ...inventory.ExecutorID) inventory.InventorySnapshot {
	t.Helper()
	executors := make([]inventory.ExecutorSnapshot, 0)
	for _, id := range []inventory.ExecutorID{inventory.ExecutorCodex, inventory.ExecutorOpenCode, inventory.ExecutorCursorAgent} {
		state := inventory.AvailabilityMissing
		for _, wanted := range available {
			if wanted == id {
				state = inventory.AvailabilityAvailable
			}
		}
		executors = append(executors, inventory.ExecutorSnapshot{ID: id, Availability: state, CredentialState: inventory.CredentialUnknown})
	}
	snapshot, err := inventory.NewSnapshot("", executors)
	if err != nil {
		t.Fatalf("NewSnapshot() error = %v", err)
	}
	return snapshot
}

func TestParseRoutingTableReadsTheLaneTableOnly(t *testing.T) {
	t.Parallel()
	table, err := ParseRoutingTable([]byte(routingTableFixture))
	if err != nil {
		t.Fatalf("ParseRoutingTable() error = %v", err)
	}
	if len(table.Rows) != 5 || !strings.HasPrefix(table.Digest, "table:") {
		t.Fatalf("table = %#v", table)
	}
	if row, ok := table.Row(ComplexityLow, DomainFrontend); !ok || row.Executor != inventory.ExecutorCursorAgent || row.Model != "composer-2.5" {
		t.Fatalf("Row(low, frontend) = %#v, %v", row, ok)
	}
	if row, ok := table.Row(ComplexityLow, DomainBackend); !ok || row.Executor != inventory.ExecutorOpenCode || row.Model != "kimi/k2.5" {
		t.Fatalf("Row(low, backend) = %#v, %v", row, ok)
	}
	if row, ok := table.Row(ComplexityCritical, DomainDocs); !ok || row.Executor != ExecutorSelf {
		t.Fatalf("Row(critical, docs) = %#v, %v", row, ok)
	}
	again, _ := ParseRoutingTable([]byte(strings.Replace(routingTableFixture, "gpt-5.6-sol", "gpt-5.6-luna", 1)))
	if again.Digest == table.Digest {
		t.Fatal("digest must change when a model changes")
	}
}

func TestParseRoutingTableRejectsBrokenRows(t *testing.T) {
	t.Parallel()
	cases := map[string]struct{ edit, want string }{
		"self below critical": {"| medium | * | codex | gpt-5.6-terra | subscription |", "self is only allowed"},
		"unknown lane":        {"| low | frontend | cursor-agent | composer-2.5 | subscription |", "is not low|medium|high|critical"},
		"unknown domain":      {"| low | frontend | cursor-agent | composer-2.5 | subscription |", `domain "web" is unknown`},
		"placeholder model":   {"| high | * | codex | gpt-5.6-sol | subscription |", "names no exact model"},
		"duplicate row":       {"| low | frontend | cursor-agent | composer-2.5 | subscription |", "already has a row"},
	}
	replacements := map[string]string{
		"self below critical": "| medium | * | self | — | host |",
		"unknown lane":        "| tiny | frontend | cursor-agent | composer-2.5 | subscription |",
		"unknown domain":      "| low | web | cursor-agent | composer-2.5 | subscription |",
		"placeholder model":   "| high | * | codex | `<strongest model>` | subscription |",
		"duplicate row":       "| low | * | cursor-agent | composer-2.5 | subscription |",
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			payload := strings.Replace(routingTableFixture, tc.edit, replacements[name], 1)
			_, err := ParseRoutingTable([]byte(payload))
			if !errors.Is(err, ErrRoutingTableInvalid) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want ErrRoutingTableInvalid containing %q", err, tc.want)
			}
		})
	}
	if _, err := ParseRoutingTable([]byte("# nothing here\n")); !errors.Is(err, ErrRoutingTableInvalid) {
		t.Fatalf("no table error = %v", err)
	}
}

func TestRoutingTableGenerationEscalatesOneRowUp(t *testing.T) {
	t.Parallel()
	table, err := ParseRoutingTable([]byte(routingTableFixture))
	if err != nil {
		t.Fatal(err)
	}
	tasks := []GenerationTask{
		{ID: "task_1", Domain: DomainBackend, Complexity: ComplexityLow},
		{ID: "task_2", Domain: DomainFrontend, Complexity: ComplexityLow},
		{ID: "task_3", Domain: DomainBackend, Complexity: ComplexityHigh},
		{ID: "task_4", Domain: DomainBackend, Complexity: ComplexityLow},
	}
	input := TableGenerationInput{
		Snapshot: tableSnapshot(t, inventory.ExecutorCodex, inventory.ExecutorOpenCode, inventory.ExecutorCursorAgent),
		Tasks:    tasks, TaskSetDigest: hexDigestFixture("plan"), WorkspaceIdentityDigest: digestFixture("workspace"),
	}
	generation, err := table.Generation(input)
	if err != nil {
		t.Fatalf("Generation() error = %v", err)
	}
	if generation.CatalogGeneration != table.Digest || generation.PolicyVersion != tablePolicyVersion || generation.Digest == "" || len(generation.Cells) != 3 {
		t.Fatalf("generation = %#v", generation)
	}
	byCell := map[cellKey]RoutingCell{}
	for _, cell := range generation.Cells {
		byCell[cellKey{cell.Domain, cell.Complexity}] = cell
	}
	backendLow := byCell[cellKey{DomainBackend, ComplexityLow}]
	if backendLow.Selected.ExecutorID != inventory.ExecutorOpenCode || backendLow.Selected.ModelID != "kimi/k2.5" || backendLow.Selected.Reasoning != "low" ||
		len(backendLow.Fallbacks) != 1 || backendLow.Fallbacks[0].ModelID != "gpt-5.6-terra" || backendLow.Fallbacks[0].Reasoning != "medium" ||
		len(backendLow.TaskIDs) != 2 || backendLow.TaskIDs[0] != "task_1" {
		t.Fatalf("backend/low cell = %#v", backendLow)
	}
	frontendLow := byCell[cellKey{DomainFrontend, ComplexityLow}]
	if frontendLow.Selected.ExecutorID != inventory.ExecutorCursorAgent || frontendLow.Selected.ModelID != "composer-2.5" {
		t.Fatalf("frontend/low cell = %#v", frontendLow)
	}
	backendHigh := byCell[cellKey{DomainBackend, ComplexityHigh}]
	if backendHigh.Selected.ModelID != "gpt-5.6-sol" || len(backendHigh.Fallbacks) != 1 || backendHigh.Fallbacks[0].ExecutorID != ExecutorSelf {
		t.Fatalf("backend/high cell = %#v", backendHigh)
	}
	// The graph consumes it unchanged.
	set := TaskSet{Slug: "plan", Tasks: []TaskArtifact{
		graphTaskArtifact("task_1", "pending", DomainBackend, ComplexityLow),
		graphTaskArtifact("task_2", "pending", DomainFrontend, ComplexityLow),
		graphTaskArtifact("task_3", "pending", DomainBackend, ComplexityHigh, "task_1"),
		graphTaskArtifact("task_4", "pending", DomainBackend, ComplexityLow),
	}}
	snapshot, err := set.DeliverySnapshot()
	if err != nil {
		t.Fatal(err)
	}
	input.TaskSetDigest = snapshot.Digest
	generation, err = table.Generation(input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewDeliveryGraph(snapshot, generation, graphGitSHA("head")); err != nil {
		t.Fatalf("NewDeliveryGraph() error = %v", err)
	}
}

func TestRoutingTableGenerationSkipsUnavailableExecutors(t *testing.T) {
	t.Parallel()
	table, err := ParseRoutingTable([]byte(routingTableFixture))
	if err != nil {
		t.Fatal(err)
	}
	input := TableGenerationInput{
		Snapshot:      tableSnapshot(t, inventory.ExecutorCodex),
		Tasks:         []GenerationTask{{ID: "task_1", Domain: DomainBackend, Complexity: ComplexityLow}},
		TaskSetDigest: hexDigestFixture("plan"), WorkspaceIdentityDigest: digestFixture("workspace"),
	}
	generation, err := table.Generation(input)
	if err != nil {
		t.Fatalf("Generation() error = %v", err)
	}
	cell := generation.Cells[0]
	if cell.Selected.ExecutorID != inventory.ExecutorCodex || cell.Selected.ModelID != "gpt-5.6-terra" || len(generation.Rejections) != 1 ||
		generation.Rejections[0].ExecutorID != inventory.ExecutorOpenCode || generation.Rejections[0].Code != "executor_unavailable" {
		t.Fatalf("cell = %#v, rejections = %#v", cell, generation.Rejections)
	}
	input.Snapshot = tableSnapshot(t)
	input.Tasks = []GenerationTask{{ID: "task_1", Domain: DomainBackend, Complexity: ComplexityHigh}}
	generation, err = table.Generation(input)
	if err != nil || generation.Cells[0].Selected.ExecutorID != ExecutorSelf {
		t.Fatalf("only self left: %#v, %v", generation.Cells, err)
	}
	input.Tasks = []GenerationTask{{ID: "task_1", Domain: DomainBackend, Complexity: ComplexityLow}}
	table.Rows = table.Rows[:4]
	if _, err := table.Generation(input); !errors.Is(err, ErrNoEligibleCandidate) {
		t.Fatalf("no executor at all: error = %v", err)
	}
}
