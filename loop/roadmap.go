package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/routing"
	"github.com/batuta-ai/core/worktree"
)

// RunRoadmap runs one delivery per approved phase on the current branch.
// Only a done delivery advances the chain; every other state stops it.
func RunRoadmap(ctx context.Context, opts Options) (state string, runErr error) {
	root, loader, err := roadmapLoader(opts.Workspace)
	if err != nil {
		return "", err
	}
	if opts.Stdout == nil {
		opts.Stdout = io.Discard
	}
	for {
		roadmap, err := loader.Load()
		if err != nil {
			return "", fmt.Errorf("loop: roadmap: %w", err)
		}
		blocked, err := reconcileRoadmapSupervision(ctx, root, opts, roadmap)
		if err != nil || blocked {
			return StateReviewBlocked, err
		}
		roadmap, err = loader.Load()
		if err != nil {
			return "", err
		}
		var next *routing.RoadmapPhase
		for i := range roadmap.Phases {
			if !roadmap.Phases[i].Done {
				next = &roadmap.Phases[i]
				break
			}
		}
		var runner *Runner
		if opts.Resume != "" {
			runner, err = Resume(ctx, opts)
			if err != nil {
				return "", err
			}
			defer func() { runErr = errors.Join(runErr, runner.Release()) }()
			if next == nil || runner.plan.Slug != next.Slug {
				return "", fmt.Errorf("loop: delivery %s does not belong to the first unfinished roadmap phase", opts.Resume)
			}
		} else {
			if next == nil {
				fmt.Fprintf(opts.Stdout, "roadmap %s: %s\n", roadmap.Title, StateDone)
				return StateDone, nil
			}
			state, err := roadmapPhaseState(root, *next)
			if err != nil {
				return "", err
			}
			if state != string(routing.PlanApproved) {
				fmt.Fprintf(opts.Stdout, "roadmap %s: %s — phase %d (%s)\n", roadmap.Title, StateWaitingPlan, next.Number, state)
				return StateWaitingPlan, nil
			}
			opts.Plan = next.Slug
			runner, err = New(ctx, opts)
			if err != nil {
				return "", err
			}
		}
		state, err := runner.Run(ctx)
		if errors.Is(err, ErrStopped) {
			fmt.Fprintf(opts.Stdout, "stopped after %d wave(s); resume with: batuta loop --resume %s --roadmap\n", opts.MaxWaves, runner.Delivery())
		}
		if err != nil || state != StateDone {
			return state, err
		}
		opts.Resume = ""
	}
}

// DryRunRoadmap prints phase readiness without preparing or opening deliveries.
func DryRunRoadmap(opts Options) error {
	root, loader, err := roadmapLoader(opts.Workspace)
	if err != nil {
		return err
	}
	roadmap, err := loader.Load()
	if err != nil {
		return fmt.Errorf("loop: roadmap: %w", err)
	}
	out := opts.Stdout
	if out == nil {
		out = io.Discard
	}
	fmt.Fprintf(out, "roadmap %s\n", roadmap.Title)
	for _, phase := range roadmap.Phases {
		state, err := roadmapPhaseState(root, phase)
		if err != nil {
			return err
		}
		if !phase.Done && state != string(routing.PlanApproved) {
			state = StateWaitingPlan + " (" + state + ")"
		}
		plan := "(no plan)"
		if phase.Slug != "" {
			plan = "plans/" + phase.Slug + ".md"
		}
		fmt.Fprintf(out, "  %d. %s → %s: %s\n", phase.Number, phase.Title, plan, state)
	}
	return nil
}

func roadmapLoader(workspace string) (string, *routing.RoadmapLoader, error) {
	root, err := filepath.Abs(workspace)
	if err != nil {
		return "", nil, err
	}
	loader, err := routing.NewRoadmapLoader(root)
	return root, loader, err
}

func roadmapPhaseState(root string, phase routing.RoadmapPhase) (string, error) {
	if phase.Done {
		return StateDone, nil
	}
	if phase.Slug == "" {
		return "missing", nil
	}
	if _, err := os.Lstat(filepath.Join(root, routing.PlanPath(phase.Slug))); errors.Is(err, os.ErrNotExist) {
		return "missing", nil
	} else if err != nil {
		return "", err
	}
	loader, err := routing.NewPlanLoader(root)
	if err != nil {
		return "", err
	}
	plan, err := loader.LoadPlan(phase.Slug)
	if err != nil {
		return "", fmt.Errorf("loop: phase %d plan %s: %w", phase.Number, phase.Slug, err)
	}
	return string(plan.Status), nil
}

// Check opted-in deliveries even when a caller restarts without supervision or
// the roadmap already has a tick. Old journals never acquire a new obligation.
func reconcileRoadmapSupervision(ctx context.Context, root string, opts Options, roadmap routing.Roadmap) (bool, error) {
	if _, err := os.Stat(filepath.Join(root, journal.Dir)); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	store, err := journal.Open(root)
	if err != nil {
		return false, err
	}
	ids, err := store.List()
	if err != nil {
		return false, err
	}
	type completed struct {
		observer SupervisionOptions
		opened   openedDetail
	}
	var ready []completed
	for _, id := range ids {
		observer := SupervisionOptions{Workspace: root, Delivery: id, CursorPath: filepath.Join(root, ".batuta", "supervision", id+".json"), Now: opts.Now}
		records, err := readSupervisionGateRecords(observer)
		if err != nil {
			return true, err
		}
		if len(records) == 0 || records[0].Kind != KindOpened {
			continue
		}
		var opened openedDetail
		if err := json.Unmarshal(records[0].Detail, &opened); err != nil {
			return true, err
		}
		if !opened.Supervision {
			continue
		}
		matched := false
		for _, phase := range roadmap.Phases {
			// Titles and phase numbers are editable presentation. The linked plan
			// retains its obligation even after a rename, reorder or manual tick.
			matched = matched || phase.Slug == opened.Slug
		}
		if !matched {
			continue
		}
		if opts.Resume == id && pendingFinalization(records) != nil {
			continue
		}
		var terminal terminalDetail
		// Later audit records cannot erase an implementation's review obligation.
		for i := len(records) - 1; i >= 0; i-- {
			if records[i].Kind != KindTerminal && records[i].Kind != kindFinalizing {
				continue
			}
			if err := json.Unmarshal(records[i].Detail, &terminal); err != nil {
				return true, err
			}
			if terminal.State == StateDone {
				break
			}
		}
		if terminal.State != StateDone {
			continue
		}
		gate, err := CheckSupervisionGate(ctx, observer)
		if err != nil {
			return true, err
		}
		if !gate.Cleared {
			fmt.Fprintf(opts.Stdout, "roadmap %s: %s — delivery %s: %s\n", roadmap.Title, StateReviewBlocked, id, gate.Reason)
			return true, nil
		}
		ready = append(ready, completed{observer, opened})
	}
	// No phase is ticked until every relevant durable obligation was checked.
	for _, item := range ready {
		if err := completeSupervisionRoadmapPhase(ctx, item.observer, item.opened); err != nil {
			return true, err
		}
	}
	return false, nil
}

func completeSupervisionRoadmapPhase(ctx context.Context, observer SupervisionOptions, opened openedDetail) error {
	records, err := readSupervisionGateRecords(observer)
	if err != nil {
		return err
	}
	candidate := supervisionReviewCandidateInWorkspace(observer.Workspace, observer.Delivery, records)
	if candidate == nil || candidate.ID == "" {
		return errors.New("loop: final review identity disappeared")
	}
	release, err := trySupervisionGateOwnership(filepath.Join(supervisionReviewDirectory(observer, *candidate), "ownership"))
	if err != nil {
		return err
	}
	defer release()
	gate, err := supervisionGateEvidence(ctx, observer, records, candidate, nil)
	if err != nil {
		return err
	}
	if !gate.Cleared {
		return errors.New("loop: review progression is blocked")
	}
	git, err := worktree.New(ctx, observer.Workspace)
	if err != nil {
		return err
	}
	branch, err := git.Branch(ctx)
	if err != nil {
		return err
	}
	if branch != opened.Branch {
		return errors.New("loop: progression delivery branch changed")
	}
	ancestor, err := git.IsAncestor(ctx, candidate.FinalCommit, "HEAD")
	if err != nil {
		return err
	}
	if !ancestor {
		return errors.New("loop: reviewed delivery is not on the current branch")
	}
	const relative = ".batuta/roadmap.md"
	path := filepath.Join(observer.Workspace, relative)
	committed, err := supervisionReviewGit(ctx, observer.Workspace, "show", "HEAD:"+relative)
	if err != nil {
		return err
	}
	current, err := readSupervisionFile(path, 1<<20)
	if err != nil {
		return err
	}
	staged, err := supervisionReviewGit(ctx, observer.Workspace, "diff", "--cached", "--name-only", "--no-renames", "-z", "--")
	if err != nil {
		return err
	}
	for _, name := range strings.Split(string(staged), "\x00") {
		if name != "" && name != relative {
			return errors.New("loop: foreign staged path prevents roadmap progression")
		}
	}
	// Permit only the exact interrupted tick; unrelated roadmap edits remain the
	// operator's work. TickPhase preserves all bytes outside the matching line.
	before := string(committed)
	lines := strings.Split(before, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "- [ ] ") && strings.HasSuffix(strings.TrimSpace(line), "plans/"+opened.Slug+".md") {
			lines[i] = "- [x]" + line[5:]
		}
	}
	after := strings.Join(lines, "\n")
	if string(current) != before && string(current) != after {
		return errors.New("loop: roadmap changed outside the pending progression tick")
	}
	if err := routing.TickPhase(path, opened.Slug); err != nil {
		return err
	}
	_, err = git.Commit(ctx, "chore(batuta): clear review progression for "+observer.Delivery+"\n", relative)
	return err
}
