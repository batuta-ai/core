package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/core/inventory"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/publication"
	"github.com/batuta-ai/core/routing"
)

// The mock engine: a shell script standing in for an executor CLI. It
// reads the brief from argv, finds the task by its title and behaves per
// FAKE_SCENARIO. In verify mode it answers TASK n: DONE per criterion.
const fakeExecutor = `#!/bin/sh
set -e
mode=$1; shift
model=""
while [ $# -gt 1 ]; do
  case "$1" in
    --model) model=$2; shift 2;;
    --effort) shift 2;;
    *) shift;;
  esac
done
text=$1
state="${FAKE_STATE:-/tmp}"
if [ "$mode" = "verify" ]; then
  n=$(printf '%s\n' "$text" | grep -c '^Criterion [0-9]*:' || true)
  i=1
  while [ "$i" -le "$n" ]; do echo "TASK $i: DONE"; i=$((i+1)); done
  exit 0
fi
title=$(printf '%s\n' "$text" | sed -n 's/^# Brief — //p' | head -1)
case "$title" in
  *one*) n=1;; *two*) n=2;; *three*) n=3;; *) echo "unknown task: $title" >&2; exit 9;;
esac
retry=0; printf '%s' "$text" | grep -q 'Feedback from the previous attempt' && retry=1
answered=""; answered=$(printf '%s\n' "$text" | sed -n 's/^The answer: //p' | head -1)
echo "fake executor: task $n model $model retry $retry scenario ${FAKE_SCENARIO:-default}"
mkdir -p out
case "${FAKE_SCENARIO:-default}" in
  fail-scope)
    if [ "$n" = 2 ] && [ "$retry" = 0 ]; then echo "drive-by" > outside.txt; echo "ok" > out/2.txt; exit 0; fi
    rm -f outside.txt; echo "ok" > out/$n.txt;;
  fail-proof-done)
    if [ "$n" = 1 ]; then
      echo "BATUTA-PROGRESS 1 START"
      echo "BATUTA-PROGRESS 1 DONE"
      echo "changed" > shared.txt
      exit 0
    fi
    echo "ok" > out/$n.txt;;
  always-broken)
    if [ "$n" = 1 ]; then echo "BROKEN by $model" > out/1.txt; exit 0; fi
    echo "ok" > out/$n.txt;;
  ask)
    if [ "$n" = 1 ] && [ -z "$answered" ]; then echo "BATUTA-QUESTION: which greeting?"; exit 0; fi
    if [ "$n" = 1 ]; then echo "$answered" > out/1.txt; else echo "ok" > out/$n.txt; fi;;
  slow)
    if [ "$n" = 1 ]; then sleep 30; fi
    echo "ok" > out/$n.txt;;
  conflict)
    if [ "$retry" = 0 ] && [ "$n" != 3 ]; then echo "written by task $n" > shared.txt; fi
    echo "ok" > out/$n.txt;;
  limit)
    if [ "$n" = 1 ] && [ ! -f "$state/limit-seen" ]; then touch "$state/limit-seen"; echo "Rate limit reached for $model, resets 11:10am" >&2; exit 1; fi
    echo "ok" > out/$n.txt;;
  satisfied)
    if [ "$n" = 1 ]; then exit 0; fi
    echo "ok" > out/$n.txt;;
  *)
    if [ "${FAKE_SCENARIO:-default}" = default ]; then
      criteria=$(printf '%s\n' "$text" | sed -n '/^## Acceptance criteria$/,/^## /p' | grep -c '^[0-9][0-9]*\. ' || true)
      i=1
      while [ "$i" -le "$criteria" ]; do
        echo "BATUTA-PROGRESS $i START"
        echo "BATUTA-PROGRESS $i DONE"
        i=$((i+1))
      done
    fi
    echo "ok" > out/$n.txt
    if [ "$n" = 2 ]; then git add -A >/dev/null; git commit -q -m "wip: task two"; fi;;
esac
exit 0
`

const testPlan = `# Plan — Greetings
<!-- inputs: profile.md@sha256:000000000000 routing.md@sha256:000000000000 -->

**Goal:** Prove the loop end to end with three tiny tasks.
**Created:** 2026-09-06 · **Status:** approved

## Tasks
- [ ] 1. Add greeting one — backend/low
      Scope: out/1.txt, shared.txt
      Accept: out/1.txt exists → test -f out/1.txt
- [ ] 2. Add greeting two — backend/low
      Scope: out/2.txt, shared.txt
      Accept: out/2.txt exists → test -f out/2.txt
- [ ] 3. Add greeting three — backend/medium
      Depends on: 1, 2
      Scope: out/3.txt
      Accept: all three greetings exist → test -f out/1.txt && test -f out/2.txt && test -f out/3.txt

## Decisions and context
Greetings live under out/. Nothing else matters.
`

type fixture struct {
	root   string
	skills string
	state  string
	fake   string
	git    string
	base   string
}

func setup(t *testing.T) fixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("posix shell fake executor")
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	f := fixture{root: root, skills: filepath.Join(root, "skills-batuta"), state: t.TempDir(), git: git}
	f.fake = filepath.Join(f.state, "fake-codex")
	if err := os.WriteFile(f.fake, []byte(fakeExecutor), 0o755); err != nil {
		t.Fatal(err)
	}
	// Skills: one adapter named codex pointing at the fake, one template.
	os.MkdirAll(filepath.Join(f.skills, "adapters"), 0o755)
	os.MkdirAll(filepath.Join(f.skills, "templates"), 0o755)
	adapter := "---\nname: codex\nexecutable: " + f.fake + "\nrun: " + f.fake + " run {model_flags} \"{brief}\" < /dev/null\n" +
		"run_file: " + f.fake + " run {model_flags} \"Follow the instructions in {brief_file}\" < /dev/null\n" +
		"model_flags: --model {model} --effort {effort}\nreadonly: " + f.fake + " verify --model {model} \"{prompt}\" < /dev/null\n" +
		"available: command -v codex\nmodels: codex debug models\nfinished: exit_code\nlimit_regex: \"rate limit reached|quota exceeded\"\nbrief_limit_lines: 2000\ncwd_flag: env\n---\n"
	os.WriteFile(filepath.Join(f.skills, "adapters", "codex.md"), []byte(adapter), 0o644)
	os.WriteFile(filepath.Join(f.skills, "templates", "generic.md"), []byte("# generic\n\n## Conventions for briefs\n\n- Follow the existing style.\n\nNever:\n\n- Reformat.\n\n## Verification hints for the orchestrator\n\n- hidden\n"), 0o644)

	// The repository under test.
	f.run(t, "init", "-q", "-b", "main")
	f.run(t, "config", "user.name", "t")
	f.run(t, "config", "user.email", "t@example.com")
	f.run(t, "config", "commit.gpgsign", "false")
	f.run(t, "config", "gc.auto", "0")
	f.run(t, "config", "gc.autoDetach", "false")
	f.run(t, "config", "maintenance.auto", "false")
	os.MkdirAll(filepath.Join(root, ".batuta"), 0o755)
	os.WriteFile(filepath.Join(root, "tests.sh"), []byte("#!/bin/sh\nif grep -rl BROKEN out 2>/dev/null | grep -q .; then echo 'broken files'; exit 1; fi\necho all green\n"), 0o755)
	os.WriteFile(filepath.Join(root, ".batuta", "profile.md"), []byte("# Batuta profile — demo\n\nTemplate: templates/generic.md\n\nStack: shell\nMethodology: tests first, conventional commits\nTest: sh ./tests.sh\nExecution: parallel\nWorktree: always\n"), 0o644)
	os.WriteFile(filepath.Join(root, ".batuta", "routing.md"), []byte("# Routing — demo\n\n| Lane | Domain | Executor | Model | Cost |\n|---|---|---|---|---|\n| low | * | codex | fake-low | cents |\n| medium | * | codex | fake-mid | cents |\n| high | * | codex | fake-high | cents |\n| critical | * | self | session | host |\n"), 0o644)
	os.WriteFile(filepath.Join(root, ".batuta", "plan-greetings.md"), []byte(testPlan), 0o644)
	f.run(t, "add", "-A")
	f.run(t, "commit", "-q", "-m", "chore: scaffold")
	f.base = f.run(t, "rev-parse", "HEAD")
	return f
}

func (f fixture) run(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command(f.git, append([]string{"-C", f.root}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (f fixture) snapshot() inventory.InventorySnapshot {
	snapshot, err := inventory.NewSnapshot("", []inventory.ExecutorSnapshot{{
		ID: inventory.ExecutorCodex, Availability: inventory.AvailabilityAvailable, CredentialState: inventory.CredentialConfigured,
		Version:          inventory.Evidence{Name: "version", Source: "fake", State: inventory.ResolutionResolved, Identifiers: []string{"0.0.1"}},
		Capabilities:     []inventory.Evidence{{Name: "models", Source: "fake", State: inventory.ResolutionResolved, Identifiers: []string{"fake-low", "fake-mid", "fake-high"}}},
		ProviderBindings: []inventory.ProviderBinding{{ProviderID: "codex"}, {ProviderID: "codex", ModelID: "fake-low"}, {ProviderID: "codex", ModelID: "fake-mid"}, {ProviderID: "codex", ModelID: "fake-high"}},
	}})
	if err != nil {
		panic(err)
	}
	return snapshot
}

func (f fixture) options(scenario string, out *bytes.Buffer) Options {
	clock := time.Date(2026, 9, 6, 3, 0, 0, 0, time.UTC)
	var clockMu sync.Mutex
	return Options{
		Workspace: f.root, Skills: f.skills, Plan: "greetings", Stdout: out,
		Inventory:   func(context.Context) (inventory.InventorySnapshot, error) { return f.snapshot(), nil },
		Environment: []string{"FAKE_SCENARIO=" + scenario, "FAKE_STATE=" + f.state},
		TaskTimeout: 2 * time.Minute, TestTimeout: time.Minute,
		LimitWaitDefault: time.Second, LimitBuffer: time.Millisecond,
		Sleep: func(context.Context, time.Duration) error { return nil },
		Now: func() time.Time {
			clockMu.Lock()
			defer clockMu.Unlock()
			clock = clock.Add(time.Second)
			return clock
		},
	}
}

func (f fixture) commitsSince(t *testing.T, base string) []string {
	t.Helper()
	out := f.run(t, "log", "--reverse", "--format=%s", base+"..HEAD")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

func (f fixture) worktrees(t *testing.T) []string {
	t.Helper()
	entries, _ := os.ReadDir(filepath.Join(f.root, ".batuta", "worktrees"))
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func readJournal(t *testing.T, f fixture, delivery string) []journal.Record {
	t.Helper()
	store, err := journal.Open(f.root)
	if err != nil {
		t.Fatal(err)
	}
	records, err := store.Read(delivery)
	if err != nil {
		t.Fatalf("journal %s: %v", delivery, err)
	}
	return records
}

func kinds(records []journal.Record) map[journal.Kind]int {
	counts := map[journal.Kind]int{}
	for _, record := range records {
		counts[record.Kind]++
	}
	return counts
}

func TestDryRunShowsDependencySafeWaves(t *testing.T) {
	f := setup(t)
	var out bytes.Buffer
	r, err := New(context.Background(), f.options("default", &out))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	preview, err := r.DryRun()
	if err != nil {
		t.Fatalf("DryRun() error = %v", err)
	}
	if len(preview.Waves) != 2 || len(preview.Waves[0].Tasks) != 2 || len(preview.Waves[1].Tasks) != 1 || preview.Waves[1].Tasks[0].ID != "task_3" {
		t.Fatalf("waves = %#v", preview.Waves)
	}
	if preview.Waves[0].Tasks[0].Executor != "codex" || preview.Waves[0].Tasks[0].Model != "fake-low" || preview.Waves[1].Tasks[0].Model != "fake-mid" ||
		strings.Join(preview.Waves[0].Tasks[0].Fallbacks, ",") != "codex/fake-mid" {
		t.Fatalf("routing in preview = %#v", preview.Waves)
	}
	var rendered bytes.Buffer
	PrintPreview(&rendered, preview)
	if !strings.Contains(rendered.String(), "wave 2") || !strings.Contains(rendered.String(), "sh ./tests.sh") {
		t.Fatalf("rendered preview:\n%s", rendered.String())
	}
	// Nothing was journaled or written by a dry run.
	if _, err := os.Stat(filepath.Join(f.root, ".batuta", "journal", r.Delivery()+".jsonl")); !os.IsNotExist(err) {
		t.Fatal("dry run must not journal")
	}
	if status := f.run(t, "status", "--porcelain"); status != "" {
		t.Fatalf("dry run dirtied the tree:\n%s", status)
	}
}

type commandRunnerFunc func(context.Context, publication.Command) (publication.CommandResult, error)

func (f commandRunnerFunc) Run(ctx context.Context, command publication.Command) (publication.CommandResult, error) {
	return f(ctx, command)
}

func TestLoopJournalsProgressWhileTheExecutorRuns(t *testing.T) {
	t.Parallel()
	f := setup(t)
	var out bytes.Buffer
	opts := f.options("default", &out)
	opts.Parallel = 1
	var r *Runner
	observed := 0
	opts.Runner = commandRunnerFunc(func(ctx context.Context, command publication.Command) (publication.CommandResult, error) {
		if command.Executable == f.fake && len(command.Args) > 0 && command.Args[0] == "run" {
			for _, state := range []string{"START", "DONE"} {
				if command.Observer == nil {
					return publication.CommandResult{ExitCode: -1}, errors.New("executor has no stdout observer")
				}
				if _, err := fmt.Fprintf(command.Observer, "BATUTA-PROGRESS 1 %s\n", state); err != nil {
					return publication.CommandResult{ExitCode: -1}, err
				}
				records, err := r.store.Read(r.Delivery())
				if err != nil {
					return publication.CommandResult{ExitCode: -1}, err
				}
				last := records[len(records)-1]
				want := fmt.Sprintf(`{"criterion":1,"execution":1,"state":%q}`, state)
				if last.Kind != journal.Kind("task_progress") || string(last.Detail) != want {
					t.Errorf("before executor return: last record = %s %s, want task_progress %s", last.Kind, last.Detail, want)
					continue
				}
				var graph routing.DeliveryGraph
				if err := json.Unmarshal(last.Graph, &graph); err != nil {
					return publication.CommandResult{ExitCode: -1}, err
				}
				task, found := graph.Task(last.TaskID)
				if !found || task.State != routing.GraphTaskRunning || last.At.IsZero() {
					t.Errorf("progress record lacks a running task graph or timestamp: %#v", last)
				}
				observed++
			}
		}
		return (publication.ExecRunner{}).Run(ctx, command)
	})
	var err error
	r, err = New(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := r.Run(context.Background()); err != nil || state != StateDone {
		t.Fatalf("Run() = %s, %v\n%s", state, err, out.String())
	}
	if observed != 6 {
		t.Fatalf("observed %d progress records during execution, want 6", observed)
	}
}

func TestLoopDeliversAThreeTaskPlanWithADependency(t *testing.T) {
	f := setup(t)
	var out bytes.Buffer
	r, err := New(context.Background(), f.options("default", &out))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	state, err := r.Run(context.Background())
	if err != nil || state != StateDone {
		t.Fatalf("Run() = %s, %v\n%s", state, err, out.String())
	}
	commits := f.commitsSince(t, f.base)
	if len(commits) != 4 || !strings.HasPrefix(commits[0], "feat: add greeting") || !strings.HasPrefix(commits[2], "feat: add greeting three") || !strings.HasPrefix(commits[3], "chore(batuta): greetings — loop done") {
		t.Fatalf("commits = %q\n%s", commits, out.String())
	}
	for _, name := range []string{"1", "2", "3"} {
		if content, err := os.ReadFile(filepath.Join(f.root, "out", name+".txt")); err != nil || strings.TrimSpace(string(content)) != "ok" {
			t.Fatalf("out/%s.txt = %q, %v", name, content, err)
		}
	}
	// The integration layer stamped each commit with the task trailer; the
	// candidate messages carried no trailer of their own.
	body := f.run(t, "log", "--format=%B", f.base+"..HEAD~1")
	if strings.Count(body, "Batuta-Task: task_") != 3 || !strings.Contains(body, "Plan greetings, task_3") {
		t.Fatalf("commit bodies:\n%s", body)
	}
	plan, _ := os.ReadFile(filepath.Join(f.root, ".batuta", "plan-greetings.md"))
	if strings.Count(string(plan), "- [x]") != 3 || !strings.Contains(string(plan), "**Status:** done") {
		t.Fatalf("plan after run:\n%s", plan)
	}
	work, _ := os.ReadFile(filepath.Join(f.root, "WORK.md"))
	if strings.Count(string(work), "- [x] Add greeting") != 3 || !strings.Contains(string(work), "codex (fake-mid)") {
		t.Fatalf("WORK.md:\n%s", work)
	}
	if wts := f.worktrees(t); len(wts) != 0 {
		t.Fatalf("worktrees left behind: %v", wts)
	}
	if status := f.run(t, "status", "--porcelain"); status != "" {
		t.Fatalf("tree dirty after run:\n%s", status)
	}
	records := readJournal(t, f, r.Delivery())
	counts := kinds(records)
	progress := map[string][]string{}
	running := map[string]bool{}
	for _, record := range records {
		switch record.Kind {
		case KindStarted:
			running[record.TaskID] = true
		case KindFinished:
			running[record.TaskID] = false
		case journal.Kind("task_progress"):
			if !running[record.TaskID] {
				t.Fatalf("progress outside executor start/finish: %#v", record)
			}
			progress[record.TaskID] = append(progress[record.TaskID], string(record.Detail))
		}
	}
	for _, taskID := range []string{"task_1", "task_2", "task_3"} {
		want := `{"criterion":1,"execution":1,"state":"START"}|{"criterion":1,"execution":1,"state":"DONE"}`
		if got := strings.Join(progress[taskID], "|"); got != want {
			t.Errorf("%s progress = %s, want %s", taskID, got, want)
		}
	}
	if counts[KindOpened] != 1 || counts[KindWave] != 2 || counts[KindCandidate] != 3 || counts[KindSettled] != 2 || counts[KindTerminal] != 1 || counts[KindFailure] != 0 {
		t.Fatalf("journal kinds = %v", counts)
	}
	trail, err := os.ReadFile(filepath.Join(f.root, ".batuta", "runs", "2026-09-06-greetings-task-3.md"))
	if err != nil || !strings.Contains(string(trail), "### Brief") || !strings.Contains(string(trail), "✅ approved") {
		t.Fatalf("trail = %v\n%s", err, trail)
	}
	var dashboard bytes.Buffer
	if err := Dashboard(f.root, r.Delivery(), &dashboard); err != nil || strings.Count(dashboard.String(), "integrated") != 3 {
		t.Fatalf("Dashboard() = %v\n%s", err, dashboard.String())
	}
	var trailOut bytes.Buffer
	if err := Trail(f.root, "", &trailOut); err != nil || !strings.Contains(trailOut.String(), "delivery_terminal") {
		t.Fatalf("Trail() = %v\n%s", err, trailOut.String())
	}
	// A second run finds nothing to do: every task is ticked.
	r2, err := New(context.Background(), f.options("default", &out))
	if err == nil {
		preview, _ := r2.DryRun()
		if len(preview.Waves) != 0 {
			t.Fatalf("second dry run still has waves: %#v", preview.Waves)
		}
	} else if !strings.Contains(err.Error(), "Status: done") {
		t.Fatalf("second New() error = %v", err)
	}
}

func TestLoopResumesAfterAStopBetweenWaves(t *testing.T) {
	f := setup(t)
	var out bytes.Buffer
	opts := f.options("default", &out)
	opts.MaxWaves = 1
	r, err := New(context.Background(), opts)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := r.Run(context.Background()); !errors.Is(err, ErrStopped) {
		t.Fatalf("Run() error = %v, want ErrStopped\n%s", err, out.String())
	}
	if commits := f.commitsSince(t, f.base); len(commits) != 2 {
		t.Fatalf("after wave 1: commits = %q", commits)
	}
	if counts := kinds(readJournal(t, f, r.Delivery())); counts[journal.Kind("task_progress")] != 4 {
		t.Fatalf("progress before resume = %d, want 4", counts[journal.Kind("task_progress")])
	}
	// A new delivery is refused while this one is open.
	if _, err := New(context.Background(), f.options("default", &out)); err == nil || !strings.Contains(err.Error(), "--resume") {
		t.Fatalf("New() with an open delivery error = %v", err)
	}
	resumeOpts := f.options("default", &out)
	resumeOpts.Resume = r.Delivery()
	resumed, err := Resume(context.Background(), resumeOpts)
	if err != nil {
		t.Fatalf("Resume() error = %v\n%s", err, out.String())
	}
	if state, err := resumed.Run(context.Background()); err != nil || state != StateDone {
		t.Fatalf("resumed Run() = %s, %v\n%s", state, err, out.String())
	}
	if commits := f.commitsSince(t, f.base); len(commits) != 4 {
		t.Fatalf("after resume: commits = %q", commits)
	}
	records := readJournal(t, f, r.Delivery())
	if counts := kinds(records); counts[KindInterrupted] != 1 || counts[KindTerminal] != 1 || counts[KindOpened] != 1 {
		t.Fatalf("journal kinds = %v", counts)
	}
}

func TestLoopResumesAnExecutorKilledMidRun(t *testing.T) {
	f := setup(t)
	var out bytes.Buffer
	opts := f.options("slow", &out)
	opts.Parallel = 1
	opts.Runner = commandRunnerFunc(func(ctx context.Context, command publication.Command) (publication.CommandResult, error) {
		if command.Executable == f.fake && len(command.Args) > 0 && command.Args[0] == "run" {
			if _, err := fmt.Fprintln(command.Observer, "BATUTA-PROGRESS 1 START"); err != nil {
				return publication.CommandResult{ExitCode: -1}, err
			}
		}
		return (publication.ExecRunner{}).Run(ctx, command)
	})
	r, err := New(context.Background(), opts)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	state, err := r.Run(ctx)
	if err != nil || state != StateCanceled {
		t.Fatalf("Run() = %s, %v\n%s", state, err, out.String())
	}
	if status := f.run(t, "status", "--porcelain"); status != "" {
		t.Fatalf("canceled run left the tree dirty:\n%s", status)
	}
	if counts := kinds(readJournal(t, f, r.Delivery())); counts[journal.Kind("task_progress")] != 1 {
		t.Fatalf("progress before resume = %d, want 1", counts[journal.Kind("task_progress")])
	}
	resumeOpts := f.options("default", &out)
	resumeOpts.Resume = r.Delivery()
	resumed, err := Resume(context.Background(), resumeOpts)
	if err != nil {
		t.Fatalf("Resume() error = %v\n%s", err, out.String())
	}
	if state, err := resumed.Run(context.Background()); err != nil || state != StateDone {
		t.Fatalf("resumed Run() = %s, %v\n%s", state, err, out.String())
	}
	records := readJournal(t, f, r.Delivery())
	var interrupted bool
	for _, record := range records {
		if record.Kind == KindFailure && strings.Contains(string(record.Detail), blockerInterrupted) && strings.Contains(string(record.Detail), `"same_runtime":true`) {
			interrupted = true
		}
	}
	if !interrupted {
		t.Fatalf("no interrupted failure with a same-runtime retry in the journal\n%s", out.String())
	}
	if commits := f.commitsSince(t, f.base); len(commits) != 4 {
		t.Fatalf("commits = %q", commits)
	}
}

func TestLoopRetriesInTheSameWorktreeThenSucceeds(t *testing.T) {
	f := setup(t)
	var out bytes.Buffer
	r, err := New(context.Background(), f.options("fail-scope", &out))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if state, err := r.Run(context.Background()); err != nil || state != StateDone {
		t.Fatalf("Run() = %s, %v\n%s", state, err, out.String())
	}
	records := readJournal(t, f, r.Delivery())
	var sawScope, sawReuse bool
	for _, record := range records {
		if record.Kind == KindFailure && record.TaskID == "task_2" {
			detail := string(record.Detail)
			sawScope = strings.Contains(detail, blockerScope) && strings.Contains(detail, "outside.txt")
			sawReuse = strings.Contains(detail, `"reuse_worktree":true`)
		}
	}
	if !sawScope || !sawReuse {
		t.Fatalf("scope failure not journaled with worktree reuse (scope=%v reuse=%v)\n%s", sawScope, sawReuse, out.String())
	}
	if _, err := os.Stat(filepath.Join(f.root, "outside.txt")); !os.IsNotExist(err) {
		t.Fatal("outside.txt reached the branch")
	}
	work, _ := os.ReadFile(filepath.Join(f.root, "WORK.md"))
	if !strings.Contains(string(work), "1 retry") {
		t.Fatalf("WORK.md does not tell the retry story:\n%s", work)
	}
}

func TestGateThreeNamesACriterionReportedDoneButFailing(t *testing.T) {
	f := setup(t)
	var out bytes.Buffer
	r, err := New(context.Background(), f.options("fail-proof-done", &out))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if state, err := r.Run(context.Background()); err != nil || state != StateBlocked {
		t.Fatalf("Run() = %s, %v\n%s", state, err, out.String())
	}
	for _, record := range readJournal(t, f, r.Delivery()) {
		if record.Kind == KindFailure && record.TaskID == "task_1" && strings.Contains(string(record.Detail), "criterion 1 was reported DONE but its proof failed: test -f out/1.txt") {
			return
		}
	}
	t.Fatalf("criterion reported DONE with a failed proof was not named in failure feedback\n%s", out.String())
}

func TestLoopEscalatesThenBlocksAndReportsExactly(t *testing.T) {
	f := setup(t)
	var out bytes.Buffer
	r, err := New(context.Background(), f.options("always-broken", &out))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	state, err := r.Run(context.Background())
	if err != nil || state != StateBlocked {
		t.Fatalf("Run() = %s, %v\n%s", state, err, out.String())
	}
	records := readJournal(t, f, r.Delivery())
	var models []string
	for _, record := range records {
		if record.Kind == KindStarted && record.TaskID == "task_1" {
			var detail struct {
				Model string `json:"model"`
			}
			_ = jsonUnmarshal(record.Detail, &detail)
			models = append(models, detail.Model)
		}
	}
	if strings.Join(models, ",") != "fake-low,fake-low,fake-mid" {
		t.Fatalf("task_1 runtimes = %v (want retry on fake-low, then escalation to fake-mid, then abort)\n%s", models, out.String())
	}
	commits := f.commitsSince(t, f.base)
	if len(commits) != 2 || !strings.HasPrefix(commits[0], "feat: add greeting two") || !strings.HasPrefix(commits[1], "chore(batuta): greetings — loop blocked") {
		t.Fatalf("commits = %q", commits)
	}
	plan, _ := os.ReadFile(filepath.Join(f.root, ".batuta", "plan-greetings.md"))
	if strings.Count(string(plan), "- [x]") != 1 || !strings.Contains(string(plan), "- [ ] 1.") || strings.Contains(string(plan), "**Status:** done") {
		t.Fatalf("plan after blocked run:\n%s", plan)
	}
	work, _ := os.ReadFile(filepath.Join(f.root, "WORK.md"))
	if !strings.Contains(string(work), "## Blocked") || !strings.Contains(string(work), "aborted: tests_failed") || !strings.Contains(string(work), "escalated from codex") {
		t.Fatalf("WORK.md:\n%s", work)
	}
	if !strings.Contains(out.String(), "❌ task_1") || !strings.Contains(out.String(), "⏸ task_3") {
		t.Fatalf("report does not name the aborted task and its blocked dependent:\n%s", out.String())
	}
	if wts := f.worktrees(t); len(wts) != 0 {
		t.Fatalf("worktrees left behind: %v", wts)
	}
}

func TestLoopParksAQuestionAndResumesWithTheAnswer(t *testing.T) {
	f := setup(t)
	var out bytes.Buffer
	r, err := New(context.Background(), f.options("ask", &out))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	state, err := r.Run(context.Background())
	if err != nil || state != StateWaitingInput {
		t.Fatalf("Run() = %s, %v\n%s", state, err, out.String())
	}
	ask := filepath.Join(f.root, ".batuta", "asks", "greetings-task-1.md")
	if content, err := os.ReadFile(ask); err != nil || !strings.Contains(string(content), "which greeting?") {
		t.Fatalf("ask file = %v\n%s", err, content)
	}
	if status := f.run(t, "status", "--porcelain"); status != "" {
		t.Fatalf("waiting run left the tree dirty:\n%s", status)
	}
	delivery, err := Answer(f.root, "1", "hello there")
	if err != nil || delivery != r.Delivery() {
		t.Fatalf("Answer() = %s, %v", delivery, err)
	}
	if _, err := os.Stat(ask); !os.IsNotExist(err) {
		t.Fatal("ask file still present after the answer")
	}
	resumeOpts := f.options("ask", &out)
	resumeOpts.Resume = delivery
	resumed, err := Resume(context.Background(), resumeOpts)
	if err != nil {
		t.Fatalf("Resume() error = %v\n%s", err, out.String())
	}
	if state, err := resumed.Run(context.Background()); err != nil || state != StateDone {
		t.Fatalf("resumed Run() = %s, %v\n%s", state, err, out.String())
	}
	if content, _ := os.ReadFile(filepath.Join(f.root, "out", "1.txt")); strings.TrimSpace(string(content)) != "hello there" {
		t.Fatalf("out/1.txt = %q, want the answer", content)
	}
}

func TestLoopReexecutesAConflictingCandidateOnTheNewBase(t *testing.T) {
	f := setup(t)
	var out bytes.Buffer
	r, err := New(context.Background(), f.options("conflict", &out))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if state, err := r.Run(context.Background()); err != nil || state != StateDone {
		t.Fatalf("Run() = %s, %v\n%s", state, err, out.String())
	}
	records := readJournal(t, f, r.Delivery())
	var reexecuted bool
	for _, record := range records {
		if record.Kind == KindSettled && strings.Contains(string(record.Detail), string(routing.SettlementReexecuteConflict)) {
			reexecuted = true
		}
	}
	if !reexecuted {
		t.Fatalf("no conflict re-execution in the journal\n%s", out.String())
	}
	if commits := f.commitsSince(t, f.base); len(commits) != 4 {
		t.Fatalf("commits = %q", commits)
	}
	if content, _ := os.ReadFile(filepath.Join(f.root, "shared.txt")); !strings.HasPrefix(string(content), "written by task") {
		t.Fatalf("shared.txt = %q", content)
	}
}

func TestLoopWaitsOutAUsageLimitWithoutSpendingARetry(t *testing.T) {
	f := setup(t)
	var out bytes.Buffer
	var slept []time.Duration
	opts := f.options("limit", &out)
	opts.Sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	r, err := New(context.Background(), opts)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if state, err := r.Run(context.Background()); err != nil || state != StateDone {
		t.Fatalf("Run() = %s, %v\n%s", state, err, out.String())
	}
	if len(slept) != 1 || slept[0] <= 0 || slept[0] > 25*time.Hour {
		t.Fatalf("slept = %v", slept)
	}
	counts := kinds(readJournal(t, f, r.Delivery()))
	if counts[KindLimitWait] != 1 || counts[KindFailure] != 0 {
		t.Fatalf("journal kinds = %v", counts)
	}
	work, _ := os.ReadFile(filepath.Join(f.root, "WORK.md"))
	if strings.Contains(string(work), "retry") {
		t.Fatalf("a limit wait must not read as a retry:\n%s", work)
	}
}

func TestLoopTicksATaskAlreadySatisfiedOnTheBase(t *testing.T) {
	f := setup(t)
	// out/1.txt already exists on the base: task 1's criterion holds before any executor runs.
	os.MkdirAll(filepath.Join(f.root, "out"), 0o755)
	os.WriteFile(filepath.Join(f.root, "out", "1.txt"), []byte("ok\n"), 0o644)
	f.run(t, "add", "-A")
	f.run(t, "commit", "-q", "-m", "chore: greeting one by hand")
	base := f.run(t, "rev-parse", "HEAD")
	var out bytes.Buffer
	r, err := New(context.Background(), f.options("satisfied", &out))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	state, err := r.Run(context.Background())
	if err != nil || state != StateBlocked {
		t.Fatalf("Run() = %s, %v\n%s", state, err, out.String())
	}
	plan, _ := os.ReadFile(filepath.Join(f.root, ".batuta", "plan-greetings.md"))
	if !strings.Contains(string(plan), "- [x] 1.") || !strings.Contains(string(plan), "- [x] 2.") || !strings.Contains(string(plan), "- [ ] 3.") {
		t.Fatalf("plan after run:\n%s", plan)
	}
	if !strings.Contains(out.String(), "already satisfied") {
		t.Fatalf("report:\n%s", out.String())
	}
	// The next run picks up task 3 alone, on top of the ticked tasks.
	next, err := New(context.Background(), f.options("default", &out))
	if err != nil {
		t.Fatalf("second New() error = %v", err)
	}
	if state, err := next.Run(context.Background()); err != nil || state != StateDone {
		t.Fatalf("second Run() = %s, %v\n%s", state, err, out.String())
	}
	commits := f.commitsSince(t, base)
	if len(commits) != 4 || !strings.HasPrefix(commits[0], "feat: add greeting two") || !strings.HasPrefix(commits[2], "feat: add greeting three") {
		t.Fatalf("commits = %q", commits)
	}
}

func TestPreflightRefusesDirtyTreesUnapprovedPlansAndSelfRouting(t *testing.T) {
	f := setup(t)
	var out bytes.Buffer
	os.WriteFile(filepath.Join(f.root, "scratch.txt"), []byte("x"), 0o644)
	if _, err := New(context.Background(), f.options("default", &out)); err == nil || !strings.Contains(err.Error(), "dirty") {
		t.Fatalf("dirty tree error = %v", err)
	}
	os.Remove(filepath.Join(f.root, "scratch.txt"))
	os.WriteFile(filepath.Join(f.root, "WORK.md"), []byte("# WORK\n"), 0o644)
	if _, err := New(context.Background(), f.options("default", &out)); err == nil || !strings.Contains(err.Error(), "commit WORK.md") {
		t.Fatalf("managed dirt error = %v", err)
	}
	os.Remove(filepath.Join(f.root, "WORK.md"))

	planPath := filepath.Join(f.root, ".batuta", "plan-greetings.md")
	original, _ := os.ReadFile(planPath)
	os.WriteFile(planPath, bytes.Replace(original, []byte("**Status:** approved"), []byte("**Status:** proposed"), 1), 0o644)
	f.run(t, "commit", "-q", "-am", "chore: unapprove")
	if _, err := New(context.Background(), f.options("default", &out)); err == nil || !strings.Contains(err.Error(), "approved plans only") {
		t.Fatalf("unapproved plan error = %v", err)
	}
	os.WriteFile(planPath, bytes.Replace(original, []byte("backend/medium"), []byte("security/critical"), 1), 0o644)
	f.run(t, "commit", "-q", "-am", "chore: critical task")
	if _, err := New(context.Background(), f.options("default", &out)); err == nil || !strings.Contains(err.Error(), "`self`") {
		t.Fatalf("self routing error = %v", err)
	}
}

func TestAbandonClosesAnOpenDelivery(t *testing.T) {
	f := setup(t)
	var out bytes.Buffer
	opts := f.options("default", &out)
	opts.MaxWaves = 1
	r, err := New(context.Background(), opts)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := r.Run(context.Background()); !errors.Is(err, ErrStopped) {
		t.Fatalf("Run() error = %v", err)
	}
	abandonOpts := f.options("default", &out)
	abandonOpts.Resume = r.Delivery()
	if state, err := Abandon(context.Background(), abandonOpts); err != nil || state != StateAbandoned {
		t.Fatalf("Abandon() = %s, %v", state, err)
	}
	plan, _ := os.ReadFile(filepath.Join(f.root, ".batuta", "plan-greetings.md"))
	if strings.Count(string(plan), "- [x]") != 2 {
		t.Fatalf("plan after abandon:\n%s", plan)
	}
	// A fresh delivery now runs the remaining task only.
	next, err := New(context.Background(), f.options("default", &out))
	if err != nil {
		t.Fatalf("New() after abandon error = %v", err)
	}
	preview, _ := next.DryRun()
	if len(preview.Waves) != 1 || preview.Waves[0].Tasks[0].ID != "task_3" {
		t.Fatalf("preview after abandon = %#v", preview.Waves)
	}
}

func jsonUnmarshal(payload []byte, target any) error { return json.Unmarshal(payload, target) }
