package loop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/publication"
	"github.com/batuta-ai/core/routing"
)

func supervisionGateFixture(t *testing.T) (SupervisionOptions, *journal.Store, string) {
	t.Helper()
	opts, store, spec := supervisionReviewFixture(t)
	records, err := store.Read(opts.Delivery)
	if err != nil {
		t.Fatal(err)
	}
	var opened openedDetail
	if err := json.Unmarshal(records[0].Detail, &opened); err != nil {
		t.Fatal(err)
	}
	opened.Supervision = true
	records[0].Detail, err = json.Marshal(opened)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(store.Path(opts.Delivery)); err != nil {
		t.Fatal(err)
	}
	copyAnswerDelivery(t, store, opts.Delivery, records)
	return opts, store, spec
}

func gateReview(t *testing.T, opts SupervisionOptions, spec, verdict string, complete bool) *SupervisionReviewJob {
	t.Helper()
	launches := 0
	engine := fakeSupervisionReview(t, opts, spec, &launches)
	original := engine.Runner
	engine.Runner = commandRunnerFunc(func(ctx context.Context, cmd publication.Command) (publication.CommandResult, error) {
		result, err := original.Run(ctx, cmd)
		if err == nil && len(cmd.Args) > 2 {
			err = writeSupervisionReviewEvidenceError(cmd, verdict, complete)
			result.ExitCode = map[string]int{"SHIP": 0, "FIX_BEFORE_SHIP": 2, "REWORK": 3}[verdict]
		}
		return result, err
	})
	job, err := RunSupervisionReview(context.Background(), opts, engine)
	if err != nil || job == nil || job.State != "reported" {
		t.Fatalf("review: %+v, %v", job, err)
	}
	return job
}

func TestSupervisionGateEvidenceAndJudgment(t *testing.T) {
	for _, tc := range []struct {
		verdict  string
		complete bool
	}{{"SHIP", true}, {"FIX_BEFORE_SHIP", true}, {"REWORK", true}, {"SHIP", false}} {
		t.Run(tc.verdict+map[bool]string{true: "/complete", false: "/partial"}[tc.complete], func(t *testing.T) {
			opts, _, spec := supervisionGateFixture(t)
			gate, err := CheckSupervisionGate(context.Background(), opts)
			if err != nil || !gate.Required || gate.Cleared {
				t.Fatalf("before review: %+v, %v", gate, err)
			}
			job := gateReview(t, opts, spec, tc.verdict, tc.complete)
			gate, err = CheckSupervisionGate(context.Background(), opts)
			if err != nil || gate.Cleared != (tc.verdict == "SHIP" && tc.complete) || gate.EvidenceDigest == "" {
				t.Fatalf("after review: %+v, %v", gate, err)
			}
			if job.Acceptance != "pending" {
				t.Fatal("progression became conductor acceptance")
			}
			decision := SupervisionJudgment{Delivery: opts.Delivery, ReviewID: job.ID, EvidenceDigest: gate.EvidenceDigest, Decision: "accept", Rationale: "Operator reviewed the remaining findings."}
			for _, invalid := range []SupervisionJudgment{
				{Delivery: opts.Delivery, ReviewID: job.ID, EvidenceDigest: gate.EvidenceDigest, Decision: "accept"},
				{Delivery: "other", ReviewID: job.ID, EvidenceDigest: gate.EvidenceDigest, Decision: "accept", Rationale: "Reason"},
				{Delivery: opts.Delivery, ReviewID: job.ID, EvidenceDigest: "sha256:stale", Decision: "accept", Rationale: "Reason"},
			} {
				if _, err := JudgeSupervisionGate(context.Background(), opts, invalid); err == nil {
					t.Fatalf("accepted invalid judgment: %+v", invalid)
				}
			}
			for i := 0; i < 2; i++ {
				gate, err = JudgeSupervisionGate(context.Background(), opts, decision)
				if err != nil || !gate.Cleared {
					t.Fatalf("judge replay %d: %+v, %v", i, gate, err)
				}
			}
			opts.CursorPath = filepath.Join(opts.Workspace, "restarted-cursor.json")
			gate, err = CheckSupervisionGate(context.Background(), opts)
			if err != nil || !gate.Cleared {
				t.Fatalf("restart after judgment: %+v, %v", gate, err)
			}
			decision.Decision = "reject"
			if _, err := JudgeSupervisionGate(context.Background(), opts, decision); err == nil {
				t.Fatal("conflicting replay overwrote decision")
			}
			if err := os.WriteFile(filepath.Join(job.Artifacts, "review.md"), []byte("tampered"), 0600); err != nil {
				t.Fatal(err)
			}
			gate, err = CheckSupervisionGate(context.Background(), opts)
			if err == nil || gate.Cleared {
				t.Fatalf("changed evidence cleared: %+v, %v", gate, err)
			}
			if _, err := JudgeSupervisionGate(context.Background(), opts, decision); err == nil {
				t.Fatal("tampered evidence accepted")
			}
		})
	}
}

func TestSupervisionGateRejectAndOwnership(t *testing.T) {
	for _, scenario := range []string{"reject", "runner", "ambiguous", "reviewer", "job", "delivery", "missing-report"} {
		t.Run(scenario, func(t *testing.T) {
			opts, store, spec := supervisionGateFixture(t)
			job := gateReview(t, opts, spec, "SHIP", true)
			gate, err := CheckSupervisionGate(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			decision := SupervisionJudgment{Delivery: opts.Delivery, ReviewID: job.ID, EvidenceDigest: gate.EvidenceDigest, Decision: "accept", Rationale: "Reviewed exact evidence."}
			switch scenario {
			case "reject":
				decision.Decision = "reject"
			case "runner":
				owner, err := acquireDeliveryOwnership(context.Background(), opts.Workspace, opts.Delivery, opts.Now())
				if err != nil {
					t.Fatal(err)
				}
				defer owner.stop()
			case "ambiguous":
				if err := os.WriteFile(filepath.Join(opts.Workspace, journal.Dir, "unknown.lock"), []byte("broken"), 0600); err != nil {
					t.Fatal(err)
				}
			case "reviewer":
				release, err := acquireSupervisionReviewOwnership(context.Background(), filepath.Join(supervisionReviewDirectory(opts, *job), "ownership"))
				if err != nil {
					t.Fatal(err)
				}
				defer release()
			case "job":
				job.Attempts++
				if err := writeSupervisionJSON(filepath.Join(supervisionReviewDirectory(opts, *job), "job.json"), job); err != nil {
					t.Fatal(err)
				}
			case "delivery":
				records, err := store.Read(opts.Delivery)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.Append(opts.Delivery, records[len(records)-1]); err != nil {
					t.Fatal(err)
				}
			case "missing-report":
				if err := os.Remove(filepath.Join(job.Artifacts, "review.md")); err != nil {
					t.Fatal(err)
				}
			}
			result, err := JudgeSupervisionGate(context.Background(), opts, decision)
			if scenario == "reject" {
				if err != nil || result.Cleared {
					t.Fatalf("reject: %+v, %v", result, err)
				}
				result, err = CheckSupervisionGate(context.Background(), opts)
				if err != nil || result.Cleared {
					t.Fatalf("restarted reject: %+v, %v", result, err)
				}
			} else if err == nil || result.Cleared {
				t.Fatalf("invalid judgment released gate: %+v, %v", result, err)
			}
		})
	}
}

func TestSupervisionGateLaunchRecoveryAndLegacy(t *testing.T) {
	opts, _, spec := supervisionGateFixture(t)
	launches := 0
	engine := fakeSupervisionReview(t, opts, spec, &launches)
	original := engine.Runner
	interrupted := errors.New("crash after launch intent")
	engine.Runner = commandRunnerFunc(func(ctx context.Context, cmd publication.Command) (publication.CommandResult, error) {
		if len(cmd.Args) > 2 {
			panic(interrupted)
		}
		return original.Run(ctx, cmd)
	})
	func() {
		defer func() {
			if got := recover(); got != interrupted {
				t.Fatalf("crash: %v", got)
			}
		}()
		_, _ = RunSupervisionReview(context.Background(), opts, engine)
	}()
	job, err := RunSupervisionReview(context.Background(), opts, engine)
	if err != nil || job.State != "uncertain" || job.Attempts != 1 || launches != 0 {
		t.Fatalf("launch recovery: %+v, %v", job, err)
	}
	gate, err := CheckSupervisionGate(context.Background(), opts)
	if err != nil || gate.Cleared {
		t.Fatalf("uncertain gate: %+v, %v", gate, err)
	}
	legacy, _, _ := supervisionReviewFixture(t)
	gate, err = CheckSupervisionGate(context.Background(), legacy)
	if err != nil || gate.Required || !gate.Cleared {
		t.Fatalf("legacy gate: %+v, %v", gate, err)
	}
}

func TestSupervisionGateBookkeepingAndRoadmapRestart(t *testing.T) {
	f := setupRoadmap(t)
	var out bytes.Buffer
	opts := f.options("default", &out)
	opts.Supervision = true
	state, err := RunRoadmap(context.Background(), opts)
	if err != nil || state != StateReviewBlocked {
		t.Fatalf("initial roadmap: %s, %v\n%s", state, err, &out)
	}
	deliveries := roadmapDeliveries(t, f)
	if len(deliveries) != 1 {
		t.Fatalf("progressed: %v", deliveries)
	}
	for delivery, opened := range deliveries {
		if !opened.Supervision {
			t.Fatal("gate identity absent from opening")
		}
		observer := SupervisionOptions{Workspace: f.root, Delivery: delivery, CursorPath: filepath.Join(f.root, ".batuta", "gate-cursor.json")}
		gate, err := CheckSupervisionGate(context.Background(), observer)
		if err != nil || !gate.Required || gate.Cleared {
			t.Fatalf("completed implementation: %+v, %v", gate, err)
		}
	}
	if roadmap := f.run(t, "show", "HEAD:.batuta/roadmap.md"); strings.Contains(roadmap, "- [x]") {
		t.Fatalf("premature roadmap completion: %s", roadmap)
	}
	opts.Supervision = false
	state, err = RunRoadmap(context.Background(), opts)
	if err != nil || state != StateReviewBlocked || len(roadmapDeliveries(t, f)) != 1 {
		t.Fatalf("restart bypass: %s, %v", state, err)
	}
}

func TestSupervisionGateBookkeepingCrash(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-commit", true: "after-commit"}[after], func(t *testing.T) {
			f := setupRoadmap(t)
			opts := f.options("default", new(bytes.Buffer))
			opts.Plan, opts.Supervision = "greetings", true
			r, err := New(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Release()
			for i := range r.graph.Tasks {
				r.graph.Tasks[i].State = routing.GraphTaskIntegrated
				r.graph.Tasks[i].IntegratedCommitSHA = f.base
			}
			if err := r.open(); err != nil {
				t.Fatal(err)
			}
			crash := errors.New("bookkeeping interrupted")
			r.git.Runner = commandRunnerFunc(func(ctx context.Context, cmd publication.Command) (publication.CommandResult, error) {
				commit := cmd.Directory == f.root && len(cmd.Args) > 0 && cmd.Args[0] == "commit"
				if commit && !after {
					panic(crash)
				}
				result, err := (publication.ExecRunner{}).Run(ctx, cmd)
				if commit && err == nil {
					panic(crash)
				}
				return result, err
			})
			func() {
				defer func() {
					if got := recover(); got != crash {
						t.Fatalf("crash: %v", got)
					}
				}()
				_, _ = r.finish(context.Background(), StateDone)
			}()
			if err := r.Release(); err != nil {
				t.Fatal(err)
			}
			opts.Supervision = false
			if state, err := RunRoadmap(context.Background(), opts); err != nil || state != StateReviewBlocked {
				t.Fatalf("crash restart: %s, %v", state, err)
			}
			opts.Resume = r.Delivery()
			if state, err := RunRoadmap(context.Background(), opts); err != nil || state != StateReviewBlocked {
				t.Fatalf("finalization recovery: %s, %v", state, err)
			}
			if len(roadmapDeliveries(t, f)) != 1 || strings.Contains(f.run(t, "show", "HEAD:.batuta/roadmap.md"), "- [x]") {
				t.Fatal("recovery advanced the roadmap before review")
			}
		})
	}
}

func supervisionGateRoadmapFixture(t *testing.T) (SupervisionOptions, *journal.Store, string) {
	t.Helper()
	opts, store, spec := supervisionGateFixture(t)
	f := fixture{root: opts.Workspace, git: "git"}
	path := filepath.Join(opts.Workspace, ".batuta", "roadmap.md")
	if err := os.WriteFile(path, []byte("# Roadmap — Reviewed delivery\n\n- [ ] 1. Deliver → plans/delivery.md\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.run(t, "add", ".batuta/roadmap.md")
	f.run(t, "commit", "-qm", "roadmap")
	records, err := store.Read(opts.Delivery)
	if err != nil {
		t.Fatal(err)
	}
	var opened openedDetail
	if err := json.Unmarshal(records[0].Detail, &opened); err != nil {
		t.Fatal(err)
	}
	opened.Roadmap, opened.Phase, opened.Branch = "Reviewed delivery", 1, f.run(t, "branch", "--show-current")
	records[0].Detail, err = json.Marshal(opened)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(store.Path(opts.Delivery)); err != nil {
		t.Fatal(err)
	}
	copyAnswerDelivery(t, store, opts.Delivery, records)
	return opts, store, spec
}

func TestSupervisionGateRoadmapJudgmentRecovery(t *testing.T) {
	for _, verdict := range []string{"SHIP", "FIX_BEFORE_SHIP", "REWORK"} {
		t.Run(verdict, func(t *testing.T) {
			observer, _, spec := supervisionGateRoadmapFixture(t)
			f := fixture{root: observer.Workspace, git: "git"}
			opts := Options{Workspace: observer.Workspace}
			job := gateReview(t, observer, spec, verdict, true)
			if verdict != "SHIP" {
				if state, err := RunRoadmap(context.Background(), opts); err != nil || state != StateReviewBlocked {
					t.Fatalf("adverse restart: %s, %v", state, err)
				}
				gate, err := CheckSupervisionGate(context.Background(), observer)
				if err != nil {
					t.Fatal(err)
				}
				judgment := SupervisionJudgment{Delivery: observer.Delivery, ReviewID: job.ID, EvidenceDigest: gate.EvidenceDigest, Decision: "accept", Rationale: "Findings evaluated for progression."}
				if _, err := JudgeSupervisionGate(context.Background(), observer, judgment); err != nil {
					t.Fatal(err)
				}
				// Simulate loss after the tick reached the working tree or index.
				if err := routing.TickPhase(filepath.Join(f.root, ".batuta/roadmap.md"), "delivery"); err != nil {
					t.Fatal(err)
				}
				if verdict == "REWORK" {
					f.run(t, "add", ".batuta/roadmap.md")
				}
			}
			before := f.run(t, "rev-parse", "HEAD")
			if state, err := RunRoadmap(context.Background(), opts); err != nil || state != StateDone {
				t.Fatalf("eligible restart: %s, %v", state, err)
			}
			after := f.run(t, "rev-parse", "HEAD")
			if before == after || !strings.Contains(f.run(t, "show", "HEAD:.batuta/roadmap.md"), "- [x]") {
				t.Fatal("eligible roadmap completion was not committed")
			}
			if state, err := RunRoadmap(context.Background(), opts); err != nil || state != StateDone || f.run(t, "rev-parse", "HEAD") != after {
				t.Fatalf("duplicate progression: %s, %v", state, err)
			}
			if err := os.WriteFile(filepath.Join(job.Artifacts, "review.md"), []byte("changed"), 0600); err != nil {
				t.Fatal(err)
			}
			if state, err := RunRoadmap(context.Background(), opts); err == nil || state != StateReviewBlocked {
				t.Fatalf("checked phase bypassed changed evidence: %s, %v", state, err)
			}
		})
	}
}

func TestSupervisionGateRoadmapIdentityCannotBypass(t *testing.T) {
	for _, changed := range []string{"title", "number", "journal-tip", "terminal-state"} {
		t.Run(changed, func(t *testing.T) {
			opts, store, spec := supervisionGateRoadmapFixture(t)
			gateReview(t, opts, spec, "REWORK", true)
			path := filepath.Join(opts.Workspace, ".batuta/roadmap.md")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			content := strings.ReplaceAll(string(data), "- [ ]", "- [x]")
			if changed == "title" {
				content = strings.ReplaceAll(content, "Reviewed delivery", "Renamed roadmap")
			} else if changed == "number" {
				content = strings.ReplaceAll(content, "- [x] 1.", "- [x] 1. Earlier → plans/earlier.md\n- [x] 2.")
			} else if changed == "journal-tip" {
				supervisionAppend(t, store, opts, KindPresenceTakenOver, `{}`)
			} else {
				supervisionAppend(t, store, opts, KindTerminal, `{"state":"abandoned"}`)
			}
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			if state, err := RunRoadmap(context.Background(), Options{Workspace: opts.Workspace}); err != nil || state != StateReviewBlocked {
				t.Fatalf("roadmap edit bypassed adverse evidence: %s, %v", state, err)
			}
		})
	}
}

func TestSupervisionGateRoadmapWaitingDoesNotCreateJournal(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".batuta"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".batuta/roadmap.md"), []byte("# Roadmap — Waiting\n\n- [ ] 1. Missing → plans/missing.md\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if state, err := RunRoadmap(context.Background(), Options{Workspace: root}); err != nil || state != StateWaitingPlan {
		t.Fatalf("waiting: %s, %v", state, err)
	}
	if _, err := os.Stat(filepath.Join(root, journal.Dir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("waiting roadmap created journal: %v", err)
	}
}

func TestSupervisionGateRoadmapPreservesOperatorWork(t *testing.T) {
	for _, changed := range []string{"branch", "staged", "roadmap"} {
		t.Run(changed, func(t *testing.T) {
			opts, _, spec := supervisionGateRoadmapFixture(t)
			gateReview(t, opts, spec, "SHIP", true)
			f := fixture{root: opts.Workspace, git: "git"}
			path := filepath.Join(opts.Workspace, ".batuta/roadmap.md")
			switch changed {
			case "branch":
				f.run(t, "checkout", "-qb", "other")
			case "staged":
				if err := os.WriteFile(filepath.Join(opts.Workspace, "source.txt"), []byte("operator change"), 0600); err != nil {
					t.Fatal(err)
				}
				f.run(t, "add", "source.txt")
			case "roadmap":
				if err := os.WriteFile(path, []byte("# Roadmap — Operator edit\n\n- [ ] 1. Deliver → plans/delivery.md\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before := f.run(t, "rev-parse", "HEAD")
			index := f.run(t, "diff", "--cached")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if state, err := RunRoadmap(context.Background(), Options{Workspace: opts.Workspace}); err == nil || state != StateReviewBlocked {
				t.Fatalf("changed workspace progressed: %s, %v", state, err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(data, after) || f.run(t, "rev-parse", "HEAD") != before || f.run(t, "diff", "--cached") != index {
				t.Fatal("progression changed operator work")
			}
		})
	}
}

func TestSupervisionGateFailedReviewStaysBlocked(t *testing.T) {
	opts, _, spec := supervisionGateFixture(t)
	launches := 0
	engine := fakeSupervisionReview(t, opts, spec, &launches)
	original := engine.Runner
	engine.Runner = commandRunnerFunc(func(ctx context.Context, cmd publication.Command) (publication.CommandResult, error) {
		if len(cmd.Args) > 2 {
			launches++
			return publication.CommandResult{ExitCode: 1}, errors.New("review failed")
		}
		return original.Run(ctx, cmd)
	})
	for i := 0; i < 2; i++ {
		job, err := RunSupervisionReview(context.Background(), opts, engine)
		if err != nil || job.State != "failed" || job.Attempts != 1 || launches != 1 {
			t.Fatalf("failed review replay: %+v, launches=%d, %v", job, launches, err)
		}
		gate, err := CheckSupervisionGate(context.Background(), opts)
		if err != nil || gate.Cleared {
			t.Fatalf("failed review progression: %+v, %v", gate, err)
		}
		judgment := SupervisionJudgment{Delivery: opts.Delivery, ReviewID: job.ID, EvidenceDigest: "sha256:unavailable", Decision: "accept", Rationale: "Cannot substitute for absent evidence."}
		if _, err := JudgeSupervisionGate(context.Background(), opts, judgment); err == nil {
			t.Fatal("accepted unavailable review evidence")
		}
	}
}

func TestSupervisionGateSavedJudgmentIntegrity(t *testing.T) {
	for _, changed := range []string{"job", "delivery", "journal-chain", "snapshot", "spec", "stale-owner", "judgment"} {
		t.Run(changed, func(t *testing.T) {
			opts, store, spec := supervisionGateFixture(t)
			job := gateReview(t, opts, spec, "REWORK", true)
			gate, err := CheckSupervisionGate(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			judgment := SupervisionJudgment{Delivery: opts.Delivery, ReviewID: job.ID, EvidenceDigest: gate.EvidenceDigest, Decision: "accept", Rationale: "Explicit operator review."}
			if _, err := JudgeSupervisionGate(context.Background(), opts, judgment); err != nil {
				t.Fatal(err)
			}
			switch changed {
			case "job":
				job.Attempts++
				err = writeSupervisionJSON(filepath.Join(supervisionReviewDirectory(opts, *job), "job.json"), job)
			case "delivery":
				var records []journal.Record
				records, err = store.Read(opts.Delivery)
				if err == nil {
					_, err = store.Append(opts.Delivery, records[len(records)-1])
				}
			case "journal-chain":
				var data []byte
				data, err = os.ReadFile(store.Path(opts.Delivery))
				if err == nil {
					err = os.WriteFile(store.Path(opts.Delivery), bytes.Replace(data, []byte(`"supervision":true`), []byte(`"supervision":false`), 1), 0600)
				}
			case "snapshot":
				err = os.WriteFile(filepath.Join(job.Snapshot, "source.txt"), []byte("changed"), 0600)
			case "spec":
				err = os.WriteFile(job.Spec, []byte("changed"), 0600)
			case "stale-owner":
				err = writeSupervisionJSON(filepath.Join(opts.Workspace, journal.Dir, opts.Delivery+".lock"), presenceLock{PID: 123, Host: "unknown", RefreshedAt: opts.Now().Add(-time.Hour)})
			case "judgment":
				changed := judgment
				changed.Rationale = " "
				err = writeSupervisionJSON(filepath.Join(supervisionReviewDirectory(opts, *job), "progression.json"), changed)
			}
			if err != nil {
				t.Fatal(err)
			}
			if gate, err := CheckSupervisionGate(context.Background(), opts); err == nil || gate.Cleared {
				t.Fatalf("saved judgment bypassed changed %s: %+v, %v", changed, gate, err)
			}
			if _, err := JudgeSupervisionGate(context.Background(), opts, judgment); err == nil {
				t.Fatal("stale decision replay succeeded")
			}
		})
	}
}

func TestSupervisionGateCorrectionProposalDoesNotClearParent(t *testing.T) {
	opts, store, spec := supervisionGateRoadmapFixture(t)
	job := gateReview(t, opts, spec, "FIX_BEFORE_SHIP", true)
	event, err := supervisionReviewEvent(opts, job, 2)
	if err != nil {
		t.Fatal(err)
	}
	policy := SupervisionPolicy{Delivery: opts.Delivery, Action: SupervisionProposeCorrection, Ownership: "approved_correction", MaxAttempts: 2,
		PlanEvidence: SupervisionEvidence{Path: "correction-plan.md", Digest: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(spec)))},
		Correction:   &SupervisionCorrectionPolicy{ReviewID: job.ID, ReportDigest: event.Evidence.Digest, SpecDigest: job.SpecDigest, Delivery: "correction-one", MaxCorrections: 1},
	}
	if err := os.WriteFile(filepath.Join(opts.Workspace, "correction-plan.md"), []byte(spec), 0600); err != nil {
		t.Fatal(err)
	}
	decision, err := proposeSupervisionCorrection(opts, *event, job, &policy)
	if err != nil || decision.Outcome != "proposed" {
		t.Fatalf("proposal: %+v, %v", decision, err)
	}
	// A reserved child identity and SHIP alone do not prove the parent's findings
	// were addressed. There is no completed correction verification receipt.
	records, err := store.Read(opts.Delivery)
	if err != nil {
		t.Fatal(err)
	}
	copyAnswerDelivery(t, store, "correction-one", records)
	child := opts
	child.Delivery, child.CursorPath = "correction-one", filepath.Join(opts.Workspace, "child-cursor.json")
	gateReview(t, child, spec, "SHIP", true)
	if state, err := RunRoadmap(context.Background(), Options{Workspace: opts.Workspace}); err != nil || state != StateReviewBlocked {
		t.Fatalf("child SHIP released parent: %s, %v", state, err)
	}
}
