package routing

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/batuta-ai/core/inventory"
)

// ExecutorSelf is the conducting host itself. It only ever appears on the
// critical lane and is never probed: the host that runs the loop is there.
const ExecutorSelf inventory.ExecutorID = "self"

// DomainAny is the `*` domain of a routing row: it applies to every domain
// that has no row of its own.
const DomainAny Domain = "*"

const tablePolicyVersion = "routing-table.v1"

var (
	ErrRoutingTableInvalid = errors.New("routing: routing table is invalid")

	tableExecutorPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	complexityLadder     = []Complexity{ComplexityLow, ComplexityMedium, ComplexityHigh, ComplexityCritical}
)

// RoutingRow is one line of the `| Lane | Domain | Executor | Model |` table
// the batuta-init skill writes to `.batuta/routing.md`.
type RoutingRow struct {
	Lane     Complexity
	Domain   Domain
	Executor inventory.ExecutorID
	Model    string
	Line     int
}

// RoutingTable is the user's confirmed routing decision for a project.
type RoutingTable struct {
	Rows   []RoutingRow
	Digest string
}

// ParseRoutingTable reads the first markdown table whose header carries
// Lane, Domain, Executor and Model columns (any order, other columns
// ignored). Rows name a lane, a domain or `*`, an executor and a model;
// backticks around the model are stripped. `self` is accepted only on the
// critical lane.
func ParseRoutingTable(payload []byte) (RoutingTable, error) {
	scanner := bufio.NewScanner(strings.NewReader(string(payload)))
	scanner.Buffer(make([]byte, 1024), maxTaskArtifactBytes)
	var (
		columns map[string]int
		rows    []RoutingRow
		lineNo  int
		seen    = map[string]int{}
	)
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "|") {
			if columns != nil && len(rows) > 0 {
				break
			}
			columns = nil
			continue
		}
		cells := splitTableRow(line)
		if columns == nil {
			header := map[string]int{}
			for index, cell := range cells {
				header[strings.ToLower(strings.TrimSpace(cell))] = index
			}
			if _, ok := header["lane"]; ok {
				if _, ok := header["executor"]; ok {
					if _, ok := header["model"]; ok {
						columns = header
					}
				}
			}
			continue
		}
		if isTableSeparator(cells) {
			continue
		}
		if len(cells) <= columns["model"] || len(cells) <= columns["executor"] || len(cells) <= columns["lane"] {
			return RoutingTable{}, fmt.Errorf("%w: line %d: row has fewer cells than the header", ErrRoutingTableInvalid, lineNo)
		}
		row := RoutingRow{
			Lane:     Complexity(strings.ToLower(strings.Trim(cells[columns["lane"]], "` "))),
			Domain:   DomainAny,
			Executor: inventory.ExecutorID(strings.ToLower(strings.Trim(cells[columns["executor"]], "` "))),
			Model:    strings.Trim(cells[columns["model"]], "` "),
			Line:     lineNo,
		}
		if index, ok := columns["domain"]; ok && index < len(cells) {
			if domain := strings.ToLower(strings.Trim(cells[index], "` ")); domain != "" {
				row.Domain = Domain(domain)
			}
		}
		if !row.Lane.Valid() {
			return RoutingTable{}, fmt.Errorf("%w: line %d: lane %q is not low|medium|high|critical", ErrRoutingTableInvalid, lineNo, row.Lane)
		}
		if row.Domain != DomainAny && !row.Domain.Valid() {
			return RoutingTable{}, fmt.Errorf("%w: line %d: domain %q is unknown", ErrRoutingTableInvalid, lineNo, row.Domain)
		}
		if !tableExecutorPattern.MatchString(string(row.Executor)) {
			return RoutingTable{}, fmt.Errorf("%w: line %d: executor %q is not a CLI name", ErrRoutingTableInvalid, lineNo, row.Executor)
		}
		if row.Executor == ExecutorSelf && row.Lane != ComplexityCritical {
			return RoutingTable{}, fmt.Errorf("%w: line %d: self is only allowed on the critical lane", ErrRoutingTableInvalid, lineNo)
		}
		if row.Executor != ExecutorSelf && (row.Model == "" || strings.HasPrefix(row.Model, "<")) {
			return RoutingTable{}, fmt.Errorf("%w: line %d: row names no exact model", ErrRoutingTableInvalid, lineNo)
		}
		key := string(row.Lane) + "\x00" + string(row.Domain)
		if prior, duplicate := seen[key]; duplicate {
			return RoutingTable{}, fmt.Errorf("%w: line %d: %s/%s already has a row at line %d", ErrRoutingTableInvalid, lineNo, row.Lane, row.Domain, prior)
		}
		seen[key] = lineNo
		rows = append(rows, row)
	}
	if err := scanner.Err(); err != nil {
		return RoutingTable{}, ErrRoutingTableInvalid
	}
	if len(rows) == 0 {
		return RoutingTable{}, fmt.Errorf("%w: no `| Lane | Domain | Executor | Model |` table found", ErrRoutingTableInvalid)
	}
	table := RoutingTable{Rows: rows}
	hash := sha256.New()
	for _, row := range rows {
		for _, part := range []string{string(row.Lane), string(row.Domain), string(row.Executor), row.Model} {
			writeDigestPart(hash, part)
		}
	}
	table.Digest = "table:" + hex.EncodeToString(hash.Sum(nil))
	return table, nil
}

func splitTableRow(line string) []string {
	line = strings.TrimSuffix(strings.TrimPrefix(line, "|"), "|")
	return strings.Split(line, "|")
}

func isTableSeparator(cells []string) bool {
	for _, cell := range cells {
		if strings.Trim(strings.TrimSpace(cell), ":-") != "" {
			return false
		}
	}
	return true
}

// Row returns the row for a lane and domain: the exact domain first, then
// the `*` row.
func (t RoutingTable) Row(lane Complexity, domain Domain) (RoutingRow, bool) {
	var wildcard *RoutingRow
	for index := range t.Rows {
		row := &t.Rows[index]
		if row.Lane != lane {
			continue
		}
		if row.Domain == domain {
			return *row, true
		}
		if row.Domain == DomainAny && wildcard == nil {
			wildcard = row
		}
	}
	if wildcard != nil {
		return *wildcard, true
	}
	return RoutingRow{}, false
}

// TableGenerationInput is what a CLI host has when it starts a loop: the
// plan's tasks, the inventory of this machine and the identity digests the
// delivery graph pins.
type TableGenerationInput struct {
	Snapshot                inventory.InventorySnapshot
	Tasks                   []GenerationTask
	TaskSetDigest           string
	WorkspaceIdentityDigest string
	EnclosingBudget         LoopBudgetCeiling
}

// Generation builds a RoutingGeneration straight from the table, without a
// Compozy catalog or a fit recommendation: the table is the user's decision.
// For every populated domain × complexity cell the selected runtime is the
// matching row; the fallbacks are the rows one lane up, then two, within
// the lane's fallback limit — the doctrine's "escalate one row up". A row
// whose executor the inventory does not report as available is recorded as
// a rejection and skipped; `self` is always available.
func (t RoutingTable) Generation(input TableGenerationInput) (RoutingGeneration, error) {
	if len(t.Rows) == 0 || t.Digest == "" {
		return RoutingGeneration{}, ErrRoutingTableInvalid
	}
	if err := input.Snapshot.Validate(); err != nil || input.Snapshot.Digest == "" {
		return RoutingGeneration{}, errors.New("routing: inventory snapshot is invalid")
	}
	if strings.TrimSpace(input.WorkspaceIdentityDigest) == "" || strings.TrimSpace(input.TaskSetDigest) == "" || len(input.Tasks) == 0 {
		return RoutingGeneration{}, errors.New("routing: immutable selection identity is incomplete")
	}
	executors := indexExecutors(input.Snapshot.Executors)
	generation := RoutingGeneration{
		SchemaVersion: routingGenerationSchemaVersion, PolicyVersion: tablePolicyVersion,
		WorkspaceIdentityDigest: input.WorkspaceIdentityDigest, TaskSetDigest: input.TaskSetDigest,
		InventoryDigest: input.Snapshot.Digest, CatalogGeneration: t.Digest,
		DeliveryFallbackLimit: deliveryFallbackLimit, EnclosingBudget: input.EnclosingBudget,
	}
	cells := map[cellKey][]string{}
	for _, task := range input.Tasks {
		if !task.Domain.Valid() || !task.Complexity.Valid() || !taskIDPattern.MatchString(task.ID) {
			return RoutingGeneration{}, fmt.Errorf("%w: task %q has an invalid lane", ErrRoutingTableInvalid, task.ID)
		}
		generation.Tasks = append(generation.Tasks, task)
		key := cellKey{domain: task.Domain, complexity: task.Complexity}
		cells[key] = append(cells[key], task.ID)
	}
	slices.SortFunc(generation.Tasks, func(a, b GenerationTask) int { return strings.Compare(a.ID, b.ID) })
	keys := make([]cellKey, 0, len(cells))
	for key := range cells {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(a, b cellKey) int {
		if value := strings.Compare(string(a.domain), string(b.domain)); value != 0 {
			return value
		}
		return strings.Compare(string(a.complexity), string(b.complexity))
	})
	for _, key := range keys {
		start := slices.Index(complexityLadder, key.complexity)
		candidates := make([]RuntimeCandidate, 0, len(complexityLadder)-start)
		seen := map[string]struct{}{}
		for _, lane := range complexityLadder[start:] {
			row, found := t.Row(lane, key.domain)
			if !found {
				continue
			}
			runtimeKey := string(row.Executor) + "\x00" + row.Model
			if _, duplicate := seen[runtimeKey]; duplicate {
				continue
			}
			seen[runtimeKey] = struct{}{}
			if code := tableRowRejection(row, executors); code != "" {
				generation.Rejections = append(generation.Rejections, CandidateRejection{
					Domain: key.domain, Complexity: key.complexity, ExecutorID: row.Executor,
					ProviderID: string(row.Executor), ModelID: row.Model, Code: code,
				})
				continue
			}
			candidates = append(candidates, RuntimeCandidate{
				ExecutorID: row.Executor, ProviderID: string(row.Executor), ModelID: row.Model, Reasoning: reasoningFor(lane),
			})
		}
		if len(candidates) == 0 {
			return RoutingGeneration{}, fmt.Errorf("%w: %s/%s", ErrNoEligibleCandidate, key.domain, key.complexity)
		}
		limit := fallbackLimit(key.complexity)
		fallbacks := candidates[1:min(len(candidates), 1+limit)]
		taskIDs := slices.Clone(cells[key])
		slices.Sort(taskIDs)
		generation.Cells = append(generation.Cells, RoutingCell{
			Domain: key.domain, Complexity: key.complexity, TaskIDs: taskIDs,
			Selected: candidates[0], Fallbacks: slices.Clone(fallbacks), FallbackLimit: limit, Policy: complexityPolicy(key.complexity),
		})
		generation.Rules = append(generation.Rules, RuntimeRule{
			Match:   RuntimeMatch{Domain: key.domain, Complexity: key.complexity},
			Runtime: RuntimeValue{Provider: candidates[0].ProviderID, Model: candidates[0].ModelID, Reasoning: candidates[0].Reasoning},
		})
	}
	slices.SortFunc(generation.Rejections, compareRejections)
	return finalizeGeneration(generation)
}

func tableRowRejection(row RoutingRow, executors map[inventory.ExecutorID]inventory.ExecutorSnapshot) string {
	if row.Executor == ExecutorSelf {
		return ""
	}
	executor, exists := executors[row.Executor]
	if !exists || executor.Availability != inventory.AvailabilityAvailable {
		return "executor_unavailable"
	}
	if executor.CredentialState == inventory.CredentialMissing {
		return "credential_missing"
	}
	return ""
}
