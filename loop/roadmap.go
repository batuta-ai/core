package loop

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/batuta-ai/core/routing"
)

// RunRoadmap runs one delivery per approved phase on the current branch.
// Only a done delivery advances the chain; every other state stops it.
func RunRoadmap(ctx context.Context, opts Options) (string, error) {
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
