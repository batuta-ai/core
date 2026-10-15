package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/routing"
	"github.com/batuta-ai/core/worktree"
)

const (
	PruneOrphan  = "orphan"
	PruneMissing = "missing"
	PruneRemoved = "removed"
	PruneSkipped = "skipped"
)

type PruneOptions struct {
	Apply     bool
	Now       func() time.Time
	Processes worktree.ProcessScanner
	Stopper   worktree.ProcessStopper
}

type PruneEntry struct {
	Path      string                    `json:"path"`
	Branch    string                    `json:"branch"`
	Slug      string                    `json:"slug"`
	State     string                    `json:"state"`
	Processes []worktree.StoppedProcess `json:"processes,omitempty"`
}

type PruneReport struct {
	Entries  []PruneEntry `json:"entries"`
	Warnings []string     `json:"warnings,omitempty"`
}

var pruneReadDir = os.ReadDir

// Prune reports registered task worktrees that no open delivery owns.
func Prune(ctx context.Context, workspace string, opts PruneOptions) (PruneReport, error) {
	git, err := worktree.New(ctx, workspace)
	if err != nil {
		return PruneReport{}, err
	}
	cacheHome, err := worktree.CacheHome(git.Root)
	if err != nil {
		return PruneReport{}, err
	}
	owned, err := pruneOwned(git.Root)
	if err != nil {
		return PruneReport{}, err
	}
	homes := []string{worktree.RepositoryHome(git.Root), cacheHome}
	managed, err := git.Managed(ctx, homes)
	if err != nil {
		return PruneReport{}, err
	}
	report := PruneReport{Entries: make([]PruneEntry, 0)}
	var current *PruneEntry
	warned := make(map[string]bool)
	warn := func(path string, err error) {
		message := fmt.Sprintf("process cleanup in %s: %v", path, err)
		if errors.Is(err, worktree.ErrProcessScanUnsupported) {
			message = "process cleanup: " + err.Error()
		}
		if !warned[message] {
			warned[message] = true
			report.Warnings = append(report.Warnings, message)
		}
	}
	git.Processes = opts.Processes
	git.Stopper = opts.Stopper
	git.OnProcessesStopped = func(path string, stopped []worktree.StoppedProcess, err error) {
		if current != nil && current.Path == path {
			current.Processes = append(current.Processes, stopped...)
		}
		if err != nil {
			warn(path, err)
		}
	}
	for _, wt := range managed {
		if owned[pruneKey{path: wt.Path, branch: wt.Branch}] {
			continue
		}
		state := PruneOrphan
		if wt.Missing {
			state = PruneMissing
		}
		report.Entries = append(report.Entries, PruneEntry{Path: wt.Path, Branch: wt.Branch, Slug: wt.Slug, State: state})
	}
	slices.SortFunc(report.Entries, func(a, b PruneEntry) int { return strings.Compare(a.Path, b.Path) })
	now := time.Now()
	if opts.Now != nil {
		now = opts.Now()
	}
	for name := range presenceFiles(git.Root) {
		delivery := strings.TrimSuffix(name, ".lock")
		owner, err := liveDeliveryOwner(git.Root, delivery, now)
		if err != nil {
			return PruneReport{}, fmt.Errorf("loop: presence of %s: %w", delivery, err)
		}
		if owner != nil {
			if opts.Apply {
				return PruneReport{}, fmt.Errorf("loop: cannot prune while delivery %s has a live runner (pid %d)", delivery, owner.PID)
			}
			report.Warnings = append(report.Warnings, fmt.Sprintf("delivery %s has a live runner (pid %d)", delivery, owner.PID))
		}
	}
	if !opts.Apply {
		for i := range report.Entries {
			entry := &report.Entries[i]
			if entry.State != PruneOrphan {
				continue
			}
			procs, err := git.ProcessesIn(ctx, entry.Path)
			if err != nil {
				warn(entry.Path, err)
			}
			for _, proc := range procs {
				entry.Processes = append(entry.Processes, worktree.StoppedProcess{Process: proc})
			}
		}
	}
	slices.Sort(report.Warnings)
	if opts.Apply {
		if slices.ContainsFunc(report.Entries, func(e PruneEntry) bool { return e.State == PruneMissing }) {
			// A missing entry has no directory to park or remove; only its
			// stale registration goes, and its branch stays.
			if err := git.PruneRegistry(ctx); err != nil {
				return report, fmt.Errorf("loop: prune worktree registry: %w", err)
			}
		}
		for i := range report.Entries {
			entry := &report.Entries[i]
			if entry.State == PruneMissing {
				continue
			}
			orphan, err := pruneStillOrphan(ctx, git, homes, *entry)
			if err != nil {
				return report, err
			}
			if !orphan {
				entry.State = PruneSkipped
				continue
			}
			current = entry
			// A failed scan or stop is already a warning through the
			// callback; the entry is parked and removed regardless.
			_, _ = git.StopInside(ctx, entry.Path)
			ref := "refs/batuta/parked/" + strings.TrimPrefix(entry.Branch, "batuta/")
			if _, err := git.Park(ctx, entry.Path, ref, "wip(batuta): prune "+entry.Branch); err != nil {
				return report, fmt.Errorf("loop: park %s: %w", entry.Path, err)
			}
			if err := git.Remove(ctx, entry.Path, entry.Branch); err != nil {
				return report, fmt.Errorf("loop: remove %s: %w", entry.Path, err)
			}
			entry.State = PruneRemoved
		}
		current = nil
		slices.Sort(report.Warnings)
	}
	return report, nil
}

func pruneStillOrphan(ctx context.Context, git worktree.GitProvider, homes []string, entry PruneEntry) (bool, error) {
	owned, err := pruneOwned(git.Root)
	if err != nil {
		return false, err
	}
	managed, err := git.Managed(ctx, homes)
	if err != nil {
		return false, err
	}
	if owned[pruneKey{path: entry.Path, branch: entry.Branch}] {
		return false, nil
	}
	for _, wt := range managed {
		if wt.Path == entry.Path && wt.Branch == entry.Branch {
			return !wt.Missing, nil
		}
	}
	return false, nil
}

type pruneKey struct {
	path   string
	branch string
}

func pruneOwned(root string) (map[pruneKey]bool, error) {
	ids, err := pruneJournalIDs(root)
	if err != nil {
		return nil, err
	}
	store, err := journal.Open(root)
	if err != nil {
		return nil, err
	}
	owned := make(map[pruneKey]bool)
	for _, id := range ids {
		records, err := store.Read(id)
		if err != nil {
			return nil, fmt.Errorf("loop: read journal %s: %w", id, err)
		}
		if len(records) == 0 || records[0].Kind != KindOpened {
			return nil, fmt.Errorf("loop: journal %s does not start with a valid delivery_opened", id)
		}
		var opening openedDetail
		if err := json.Unmarshal(records[0].Detail, &opening); err != nil || opening.Slug == "" {
			return nil, fmt.Errorf("loop: journal %s has an invalid delivery_opened: %v", id, err)
		}
		// Ownership takeover is an audit of recovery: it can follow the
		// terminal record and does not reopen the delivery.
		end := len(records) - 1
		for end > 0 && records[end].Kind == KindPresenceTakenOver {
			end--
		}
		last := records[end]
		if last.Kind == KindTerminal {
			var terminal terminalDetail
			if err := json.Unmarshal(last.Detail, &terminal); err != nil {
				return nil, fmt.Errorf("loop: journal %s terminal: %w", id, err)
			}
			if terminal.State == StateDone || terminal.State == StateAbandoned {
				continue
			}
		}
		for _, record := range records {
			if len(record.Graph) == 0 {
				continue
			}
			var graph routing.DeliveryGraph
			if err := json.Unmarshal(record.Graph, &graph); err != nil {
				return nil, fmt.Errorf("loop: journal %s graph: %w", id, err)
			}
			for _, task := range graph.Tasks {
				for _, attempt := range task.Attempts {
					if attempt.WorktreeRoot == "" || attempt.Execution < 1 {
						continue
					}
					branch := "batuta/" + opening.Slug + "/" + strings.ReplaceAll(task.TaskID, "_", "-") + "-e" + strconv.Itoa(attempt.Execution)
					owned[pruneKey{path: attempt.WorktreeRoot, branch: branch}] = true
				}
			}
		}
	}
	return owned, nil
}

func pruneJournalIDs(root string) ([]string, error) {
	dir := filepath.Join(root, filepath.FromSlash(journal.Dir))
	entries, err := pruneReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("loop: list journals: %w", err)
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("loop: inspect journal %s: %w", name, err)
		}
		id := strings.TrimSuffix(name, ".jsonl")
		if !info.Mode().IsRegular() || !journal.ValidDeliveryID(id) {
			return nil, fmt.Errorf("loop: invalid journal entry %s", name)
		}
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids, nil
}
