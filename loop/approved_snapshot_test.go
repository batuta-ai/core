package loop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/publication"
	"github.com/batuta-ai/core/routing"
)

const approvedSnapshotSource = "# Plan — Greetings\r\n**Goal:** Use approved bytes\r\n**Status:** proposed\r\n\r\n## Tasks\r\n- [ ] 1. Add greeting one — backend/low\r\n  Scope: out/1.txt\r\n  Accept: valid diff → git diff --check\r\n\r\n## Decisions and context\r\nOriginal context.\r\n"

// This is a synthetic executor CLI. The parser, loop, journal, gates and Git
// remain real, and this fixture has no POSIX shell or platform skip.
func TestApprovedSnapshotWorkerHelper(t *testing.T) {
	if os.Getenv("BATUTA_SNAPSHOT_WORKER") != "1" {
		return
	}
	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 || separator+2 >= len(os.Args) {
		os.Exit(10)
	}
	mode, brief := os.Args[separator+1], os.Args[len(os.Args)-1]
	if mode == "available" {
		os.Exit(0)
	}
	if mode == "models" {
		fmt.Println("fake-low\nfake-mid\nfake-high")
		os.Exit(0)
	}
	if mode == "verify" {
		for i := 1; i <= strings.Count(brief, "Criterion "); i++ {
			fmt.Printf("TASK %d: DONE\n", i)
		}
		os.Exit(0)
	}
	if os.Getenv("FAKE_SCENARIO") == "ask" && !strings.Contains(brief, "The answer:") {
		fmt.Println("BATUTA-QUESTION: which greeting?")
		os.Exit(0)
	}
	if err := os.MkdirAll("out", 0o755); err != nil {
		os.Exit(11)
	}
	if err := os.WriteFile(filepath.Join("out", "1.txt"), []byte("approved greeting\n"), 0o644); err != nil {
		os.Exit(12)
	}
	fmt.Println("BATUTA-PROGRESS 1 START\nBATUTA-PROGRESS 1 DONE")
	os.Exit(0)
}

func approvedSnapshotFixture(t *testing.T) (fixture, *ApprovedPlanSnapshot) {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{root: root, git: git, skills: t.TempDir(), state: t.TempDir(), fake: exe}
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	command := strconv.Quote(exe) + " -test.run=TestApprovedSnapshotWorkerHelper -- "
	write(filepath.Join(f.skills, "adapters", "codex.md"), "---\nname: codex\nexecutable: '"+exe+"'\nrun: '"+command+"run {model_flags} \"{brief}\" < /dev/null'\nreadonly: '"+command+"verify --model {model} \"{prompt}\" < /dev/null'\navailable: '"+command+"available ready'\nmodels: '"+command+"models ready'\nmodel_flags: --model {model} --effort {effort}\nfinished: exit_code\nbrief_limit_lines: 2000\ncwd_flag: env\n---\n")
	write(filepath.Join(f.skills, "templates", "generic.md"), "# Generic\n\n## Conventions for briefs\n\n- Preserve existing style.\n\nNever:\n\n- Edit beyond scope.\n")
	write(filepath.Join(root, ".batuta", "profile.md"), "# Profile\nTemplate: templates/generic.md\nStack: text\nMethodology: tests first\nTest: git diff --check\nExecution: sequential\nWorktree: always\n")
	write(filepath.Join(root, ".batuta", "routing.md"), "# Routing\n| Lane | Domain | Executor | Model |\n|---|---|---|---|\n| low | * | codex | fake-low |\n| medium | * | codex | fake-mid |\n| high | * | codex | fake-high |\n| critical | * | self | session |\n")
	write(filepath.Join(root, routing.PlanPath("greetings")), approvedSnapshotSource)
	f.run(t, "init", "-q", "-b", "main")
	f.run(t, "config", "user.name", "snapshot-test")
	f.run(t, "config", "user.email", "snapshot@example.com")
	f.run(t, "config", "commit.gpgsign", "false")
	f.run(t, "config", "core.autocrlf", "false")
	f.run(t, "config", "gc.auto", "0")
	f.run(t, "add", "-A")
	f.run(t, "commit", "-q", "-m", "chore: snapshot fixture")
	f.base = f.run(t, "rev-parse", "HEAD")
	content := []byte(approvedSnapshotSource)
	return f, &ApprovedPlanSnapshot{Slug: "greetings", Path: filepath.ToSlash(routing.PlanPath("greetings")), ReceiptID: "approval-exact", ContentDigest: snapshotTestDigest(content), Content: content}
}

func snapshotTestDigest(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

func snapshotTestOptions(f fixture, snapshot *ApprovedPlanSnapshot, scenario string, out *bytes.Buffer) Options {
	opts := f.options(scenario, out)
	opts.Now = nil
	opts.ApprovedSnapshot = snapshot
	opts.Environment = append(opts.Environment, "BATUTA_SNAPSHOT_WORKER=1")
	return opts
}

func TestApprovedSnapshotReviewGitHandlesLongObjectPaths(t *testing.T) {
	t.Parallel()
	f, _ := approvedSnapshotFixture(t)
	parent := t.TempDir()
	padding := 220 - len(parent) - len("review-source") - 2
	if padding < 1 || padding > 255 {
		t.Fatalf("temporary parent cannot represent the Git object-path boundary: %q", parent)
	}
	destination := filepath.Join(parent, strings.Repeat("n", padding), "review-source")
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := supervisionReviewGit(ctx, f.root, "clone", "--no-hardlinks", "--no-checkout", "--", f.root, destination); err != nil {
		t.Fatal(err)
	}
	if _, err := supervisionReviewGit(ctx, destination, "checkout", "--detach", f.base, "--"); err != nil {
		t.Fatal(err)
	}
	head, err := supervisionReviewGit(ctx, destination, "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(string(head)) != f.base {
		t.Fatalf("wrong clone head %q: %v", head, err)
	}
}

func TestApprovedSnapshotUsesOwnedExactBytes(t *testing.T) {
	t.Parallel()
	f, snapshot := approvedSnapshotFixture(t)
	receipt := "approval-exact-ação-🎵"
	snapshot.ReceiptID = receipt
	var out bytes.Buffer
	r, err := New(context.Background(), snapshotTestOptions(f, snapshot, "default", &out))
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Content[0] = '!'
	snapshot.ReceiptID = "changed-by-caller"
	if r.plan.Context != "Original context." || r.plan.Set.Digest == "" {
		t.Fatalf("wrong native plan: %+v", r.plan)
	}
	path := filepath.Join(f.root, routing.PlanPath("greetings"))
	external := []byte(strings.Replace(approvedSnapshotSource, "Original context.", "External edit.", 1))
	if err := os.WriteFile(path, external, 0o640); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	state, err := r.Run(context.Background())
	if err != nil || state != StateDone {
		t.Fatalf("Run=%s %v\n%s", state, err, out.String())
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, external) {
		t.Fatalf("editable source changed: %q %v", after, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode() != before.Mode() {
		t.Fatal("source mode changed", err)
	}
	if _, err := os.Stat(filepath.Join(f.root, ".batuta", "plans", "done", "greetings.md")); !os.IsNotExist(err) {
		t.Fatal("editable source archived", err)
	}
	if staged := f.run(t, "diff", "--cached", "--name-only"); staged != "" {
		t.Fatal("staged paths", staged)
	}
	if committed := f.run(t, "show", "HEAD:"+filepath.ToSlash(routing.PlanPath("greetings"))); committed != strings.TrimSpace(approvedSnapshotSource) {
		t.Fatal("bookkeeping committed the external edit", committed)
	}
	records := readJournal(t, f, r.Delivery())
	var opened openedDetail
	if err := json.Unmarshal(records[0].Detail, &opened); err != nil {
		t.Fatal(err)
	}
	if opened.ApprovalReceiptID != receipt || opened.PlanContentDigest != snapshotTestDigest([]byte(approvedSnapshotSource)) || opened.PlanPath != filepath.ToSlash(routing.PlanPath("greetings")) || opened.Workspace != f.root {
		t.Fatalf("incorrect opening: %+v", opened)
	}
	if kinds(records)[KindOpened] != 1 || kinds(records)[KindGates] == 0 {
		t.Fatal("missing native opening/gates")
	}
}

func TestApprovedSnapshotRejectsIncompleteAndForeignIdentity(t *testing.T) {
	for _, kind := range []string{"receipt", "receipt encoding", "receipt oversize", "receipt whitespace", "digest", "uppercase", "bytes", "empty", "oversize", "slug", "path", "traversal", "reference", "status", "delivery"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			f, snapshot := approvedSnapshotFixture(t)
			var out bytes.Buffer
			opts := snapshotTestOptions(f, snapshot, "default", &out)
			switch kind {
			case "receipt":
				snapshot.ReceiptID = ""
			case "receipt encoding":
				snapshot.ReceiptID = string([]byte{'a', 0xff})
			case "receipt oversize":
				snapshot.ReceiptID = strings.Repeat("a", 1025)
			case "receipt whitespace":
				snapshot.ReceiptID = " approval-exact"
			case "digest":
				snapshot.ContentDigest = "invalid"
			case "uppercase":
				snapshot.ContentDigest = strings.ToUpper(snapshot.ContentDigest)
			case "bytes":
				snapshot.Content = append(snapshot.Content, '\n')
			case "empty":
				snapshot.Content = nil
				snapshot.ContentDigest = snapshotTestDigest(nil)
			case "oversize":
				snapshot.Content = append(snapshot.Content, bytes.Repeat([]byte("x"), 1<<20)...)
				snapshot.ContentDigest = snapshotTestDigest(snapshot.Content)
			case "slug":
				snapshot.Slug = "foreign"
			case "path":
				snapshot.Path = ".batuta/plans/foreign.md"
			case "traversal":
				snapshot.Path = ".batuta/plans/../plans/greetings.md"
			case "reference":
				opts.Plan = "foreign"
			case "status":
				snapshot.Content = bytes.Replace(snapshot.Content, []byte("proposed"), []byte("done"), 1)
				snapshot.ContentDigest = snapshotTestDigest(snapshot.Content)
			case "delivery":
				opts.DeliveryID = "../foreign"
			}
			if _, err := New(context.Background(), opts); err == nil {
				t.Fatal("invalid snapshot accepted")
			}
			store, err := journal.Open(f.root)
			if err != nil {
				t.Fatal(err)
			}
			ids, err := store.List()
			if err != nil || len(ids) != 0 {
				t.Fatalf("invalid approval opened journal: %v %v", ids, err)
			}
		})
	}
}

func TestApprovedSnapshotResumeNeedsOriginalReceiptAndVersion(t *testing.T) {
	t.Parallel()
	f, snapshot := approvedSnapshotFixture(t)
	var out bytes.Buffer
	opts := snapshotTestOptions(f, snapshot, "ask", &out)
	r, err := New(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := r.Run(context.Background()); err != nil || state != StateWaitingInput {
		t.Fatalf("Run=%s %v\n%s", state, err, out.String())
	}
	before := readJournal(t, f, r.Delivery())
	for _, kind := range []string{"absent", "receipt", "prose", "header", "line endings"} {
		resume := snapshotTestOptions(f, snapshot, "ask", &out)
		resume.Resume = r.Delivery()
		copy := *snapshot
		copy.Content = append([]byte(nil), snapshot.Content...)
		resume.ApprovedSnapshot = &copy
		switch kind {
		case "absent":
			resume.ApprovedSnapshot = nil
		case "receipt":
			copy.ReceiptID = "another-receipt"
		case "prose":
			copy.Content = bytes.Replace(copy.Content, []byte("Original context."), []byte("New context."), 1)
			copy.ContentDigest = snapshotTestDigest(copy.Content)
		case "header":
			copy.Content = bytes.Replace(copy.Content, []byte("proposed"), []byte("approved"), 1)
			copy.ContentDigest = snapshotTestDigest(copy.Content)
		case "line endings":
			copy.Content = bytes.ReplaceAll(copy.Content, []byte("\r\n"), []byte("\n"))
			copy.ContentDigest = snapshotTestDigest(copy.Content)
		}
		if wrong, err := Resume(context.Background(), resume); err == nil {
			wrong.Release()
			t.Fatalf("accepted %s", kind)
		}
	}
	if len(readJournal(t, f, r.Delivery())) != len(before) {
		t.Fatal("failed resume changed journal")
	}
	if err := os.Remove(filepath.Join(f.root, routing.PlanPath("greetings"))); err != nil {
		t.Fatal(err)
	}
	if _, err := Answer(f.root, "1", "hello"); err != nil {
		t.Fatal(err)
	}
	resume := snapshotTestOptions(f, snapshot, "ask", &out)
	resume.Resume = r.Delivery()
	resumed, err := Resume(context.Background(), resume)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.plan.Context != "Original context." {
		t.Fatal("context changed")
	}
	if state, err := resumed.Run(context.Background()); err != nil || state != StateDone {
		t.Fatalf("resume=%s %v\n%s", state, err, out.String())
	}
	if _, err := os.Stat(filepath.Join(f.root, routing.PlanPath("greetings"))); !os.IsNotExist(err) {
		t.Fatal("source recreated", err)
	}
}

func TestApprovedSnapshotFixedDeliveryDoesNotReopen(t *testing.T) {
	t.Parallel()
	f, snapshot := approvedSnapshotFixture(t)
	var oneOut, twoOut bytes.Buffer
	opts := snapshotTestOptions(f, snapshot, "default", &oneOut)
	opts.DeliveryID = "approved-delivery"
	one, err := New(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	opts.Stdout = &twoOut
	two, err := New(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if one.Delivery() != opts.DeliveryID || two.Delivery() != opts.DeliveryID {
		t.Fatal("delivery changed")
	}
	if state, err := one.Run(context.Background()); err != nil || state != StateDone {
		t.Fatalf("Run=%s %v\n%s", state, err, oneOut.String())
	}
	before := readJournal(t, f, one.Delivery())
	if _, err := two.Run(context.Background()); err == nil {
		t.Fatal("prepared duplicate executed")
	}
	if after := readJournal(t, f, one.Delivery()); len(after) != len(before) || kinds(after)[journal.Kind(KindOpened)] != 1 {
		t.Fatal("duplicate changed journal")
	}
	if _, err := New(context.Background(), opts); err == nil {
		t.Fatal("opened ID reused")
	}
}

func TestApprovedSnapshotDuplicateNewPreservesStaleJournalAndLock(t *testing.T) {
	t.Parallel()
	f, snapshot := approvedSnapshotFixture(t)
	var out bytes.Buffer
	opts := snapshotTestOptions(f, snapshot, "default", &out)
	opts.DeliveryID = "snapshot-stale-duplicate"
	first, err := New(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := New(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := first.Run(context.Background()); err != nil || state != StateDone {
		t.Fatalf("first run %s %v", state, err)
	}
	writePresenceFixture(t, f.root, first.Delivery(), time.Now().UTC().Add(-presenceFresh-time.Second))
	journalPath := filepath.Join(f.root, journal.Dir, first.Delivery()+".jsonl")
	lockPath := filepath.Join(f.root, journal.Dir, first.Delivery()+".lock")
	before, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := duplicate.Run(context.Background()); !errors.Is(err, ErrDeliveryExists) {
		t.Fatalf("duplicate run: %v", err)
	}
	if after, err := os.ReadFile(journalPath); err != nil || !bytes.Equal(after, before) {
		t.Fatalf("duplicate mutated native journal: %v", err)
	}
	if after, err := os.ReadFile(lockPath); err != nil || !bytes.Equal(after, lock) {
		t.Fatalf("duplicate mutated old ownership marker: %v", err)
	}
}

func TestApprovedSnapshotInvalidRecoveryPreservesStaleJournalAndLock(t *testing.T) {
	for _, kind := range []string{"resume missing", "resume foreign", "abandon missing", "abandon foreign"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			f, snapshot := approvedSnapshotFixture(t)
			var out bytes.Buffer
			opts := snapshotTestOptions(f, snapshot, "ask", &out)
			r, err := New(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			if state, err := r.Run(context.Background()); err != nil || state != StateWaitingInput {
				t.Fatalf("first run %s %v", state, err)
			}
			writePresenceFixture(t, f.root, r.Delivery(), time.Now().UTC().Add(-presenceFresh-time.Second))
			journalPath := filepath.Join(f.root, journal.Dir, r.Delivery()+".jsonl")
			lockPath := filepath.Join(f.root, journal.Dir, r.Delivery()+".lock")
			before, err := os.ReadFile(journalPath)
			if err != nil {
				t.Fatal(err)
			}
			lock, err := os.ReadFile(lockPath)
			if err != nil {
				t.Fatal(err)
			}
			opts.Resume = r.Delivery()
			want := ErrSnapshotRequired
			if strings.HasSuffix(kind, "missing") {
				opts.ApprovedSnapshot = nil
			} else {
				copy := *snapshot
				copy.ReceiptID = "foreign-receipt"
				opts.ApprovedSnapshot = &copy
				want = ErrSnapshotMismatch
			}
			if strings.HasPrefix(kind, "resume") {
				_, err = Resume(context.Background(), opts)
			} else {
				_, err = Abandon(context.Background(), opts)
			}
			if !errors.Is(err, want) {
				t.Fatalf("invalid recovery: %v", err)
			}
			if after, err := os.ReadFile(journalPath); err != nil || !bytes.Equal(after, before) {
				t.Fatalf("invalid resume mutated native journal: %v", err)
			}
			if after, err := os.ReadFile(lockPath); err != nil || !bytes.Equal(after, lock) {
				t.Fatalf("invalid resume mutated old ownership marker: %v", err)
			}
		})
	}
}

func TestApprovedSnapshotAbandonRejectsInvalidDeliveryBeforeOwnership(t *testing.T) {
	t.Parallel()
	f, snapshot := approvedSnapshotFixture(t)
	var out bytes.Buffer
	opts := snapshotTestOptions(f, snapshot, "default", &out)
	opts.Resume = "../escape"
	if _, err := Abandon(context.Background(), opts); err == nil {
		t.Fatal("invalid delivery accepted")
	}
	if _, err := os.Stat(filepath.Join(f.root, ".batuta", "escape.lock.guard")); !os.IsNotExist(err) {
		t.Fatalf("invalid delivery created an ownership file outside the journal: %v", err)
	}
}

func TestApprovedSnapshotDoesNotInventApprovalForProposedSource(t *testing.T) {
	t.Parallel()
	f, _ := approvedSnapshotFixture(t)
	var out bytes.Buffer
	if _, err := New(context.Background(), f.options("default", &out)); err == nil {
		t.Fatal("proposed source without receipt accepted")
	}
}

func TestApprovedSnapshotConcurrentFixedDeliveryOpensOnce(t *testing.T) {
	t.Parallel()
	f, snapshot := approvedSnapshotFixture(t)
	var outputs [2]bytes.Buffer
	runners := make([]*Runner, 2)
	for i := range runners {
		opts := snapshotTestOptions(f, snapshot, "default", &outputs[i])
		opts.DeliveryID = "concurrent-approved"
		opts.Now = nil
		var err error
		runners[i], err = New(context.Background(), opts)
		if err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	states := make([]string, 2)
	errs := make([]error, 2)
	for i := range runners {
		wg.Add(1)
		go func(i int) { defer wg.Done(); <-start; states[i], errs[i] = runners[i].Run(context.Background()) }(i)
	}
	close(start)
	wg.Wait()
	success := 0
	for i := range states {
		if states[i] == StateDone && errs[i] == nil {
			success++
		} else if errs[i] == nil {
			t.Fatalf("duplicate result=%s %v", states[i], errs[i])
		}
	}
	if success != 1 {
		t.Fatalf("results=%v %v\n%s\n%s", states, errs, outputs[0].String(), outputs[1].String())
	}
	records := readJournal(t, f, "concurrent-approved")
	if counts := kinds(records); counts[KindOpened] != 1 || counts[KindStarted] != 1 || counts[KindTerminal] != 1 {
		t.Fatalf("duplicate native execution: %v", counts)
	}
}

func TestApprovedSnapshotFinalizationRecoveryRequiresOriginalBytes(t *testing.T) {
	for _, retry := range []string{"resume", "abandon"} {
		t.Run(retry, func(t *testing.T) {
			t.Parallel()
			f, snapshot := approvedSnapshotFixture(t)
			var out bytes.Buffer
			opts := snapshotTestOptions(f, snapshot, "default", &out)
			opts.MaxWaves = 1
			opts.KeepWorktrees = true
			r, err := New(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.Run(context.Background()); !errors.Is(err, ErrStopped) {
				t.Fatalf("stop=%v\n%s", err, out.String())
			}
			r.opts.MaxWaves = 0
			r.opts.KeepWorktrees = false
			r.git.Runner = commandRunnerFunc(func(ctx context.Context, c publication.Command) (publication.CommandResult, error) {
				if c.Directory == f.root && len(c.Args) > 0 && c.Args[0] == "commit" {
					return publication.CommandResult{ExitCode: 1}, errors.New("injected bookkeeping failure")
				}
				return (publication.ExecRunner{}).Run(ctx, c)
			})
			if state, err := r.Run(context.Background()); state != StateDone || err == nil {
				t.Fatalf("finish=%s %v", state, err)
			}
			records := readJournal(t, f, r.Delivery())
			if pendingFinalization(records) == nil {
				t.Fatal("no native recovery checkpoint")
			}
			before := len(records)
			opts.Resume = r.Delivery()
			opts.MaxWaves = 0
			opts.KeepWorktrees = false
			for _, kind := range []string{"missing", "changed"} {
				invalid := opts
				if kind == "missing" {
					invalid.ApprovedSnapshot = nil
				} else {
					copy := *snapshot
					copy.Content = bytes.ReplaceAll(snapshot.Content, []byte("Original context."), []byte("another context."))
					copy.ContentDigest = snapshotTestDigest(copy.Content)
					invalid.ApprovedSnapshot = &copy
				}
				if retry == "resume" {
					if wrong, err := Resume(context.Background(), invalid); err == nil {
						wrong.Release()
						t.Fatal("early recovery accepted", kind)
					}
				} else if _, err := Abandon(context.Background(), invalid); err == nil {
					t.Fatal("abandon recovery accepted", kind)
				}
			}
			if len(readJournal(t, f, r.Delivery())) != before {
				t.Fatal("invalid recovery appended")
			}
			source := filepath.Join(f.root, routing.PlanPath("greetings"))
			external := []byte("new unapproved external proposal\r\n")
			if retry == "resume" {
				if err := os.WriteFile(source, external, 0640); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Remove(source); err != nil {
				t.Fatal(err)
			}
			if retry == "resume" {
				resumed, err := Resume(context.Background(), opts)
				if err != nil {
					t.Fatal(err)
				}
				if resumed.plan.Context != "Original context." {
					resumed.Release()
					t.Fatal("recovery context changed")
				}
				if state, err := resumed.Run(context.Background()); err != nil || state != StateDone {
					t.Fatalf("retry=%s %v", state, err)
				}
				body, err := os.ReadFile(source)
				if err != nil || !bytes.Equal(body, external) {
					t.Fatal("recovery overwrote source", err)
				}
			} else {
				if state, err := Abandon(context.Background(), opts); err != nil || state != StateDone {
					t.Fatalf("abandon=%s %v", state, err)
				}
				if _, err := os.Stat(source); !os.IsNotExist(err) {
					t.Fatal("recovery recreated source", err)
				}
			}
			after := readJournal(t, f, r.Delivery())
			if terminalState(after) != StateDone {
				t.Fatal("recovery not completed")
			}
			for _, rec := range after[before:] {
				if rec.Kind == KindStarted {
					t.Fatal("recovery replayed executor")
				}
			}
			if index := f.run(t, "diff", "--cached", "--name-only"); index != "" {
				t.Fatal("recovery left index", index)
			}
			if _, err := os.Stat(filepath.Join(f.root, ".batuta", "plans", "done", "greetings.md")); !os.IsNotExist(err) {
				t.Fatal("recovery archived source", err)
			}
		})
	}
}

func TestApprovedSnapshotCannotUpgradeLegacyOpening(t *testing.T) {
	t.Parallel()
	f, snapshot := approvedSnapshotFixture(t)
	source := filepath.Join(f.root, routing.PlanPath("greetings"))
	approved := bytes.ReplaceAll(snapshot.Content, []byte("proposed"), []byte("approved"))
	if err := os.WriteFile(source, approved, 0640); err != nil {
		t.Fatal(err)
	}
	f.run(t, "add", "-A")
	f.run(t, "commit", "-qm", "test: legacy approval")
	var out bytes.Buffer
	opts := snapshotTestOptions(f, snapshot, "ask", &out)
	opts.ApprovedSnapshot = nil
	r, err := New(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := r.Run(context.Background()); err != nil || state != StateWaitingInput {
		t.Fatalf("legacy run=%s %v", state, err)
	}
	opts.Resume = r.Delivery()
	opts.ApprovedSnapshot = snapshot
	before := len(readJournal(t, f, r.Delivery()))
	if wrong, err := Resume(context.Background(), opts); !errors.Is(err, ErrSnapshotMismatch) {
		if wrong != nil {
			wrong.Release()
		}
		t.Fatalf("legacy upgrade=%v", err)
	}
	if len(readJournal(t, f, r.Delivery())) != before {
		t.Fatal("legacy opening changed")
	}
}

func TestApprovedSnapshotSupervisionReviewRecoveryRequiresReceipt(t *testing.T) {
	t.Parallel()
	f, snapshot := approvedSnapshotFixture(t)
	var out bytes.Buffer
	opts := snapshotTestOptions(f, snapshot, "default", &out)
	opts.Supervision = true
	r, err := New(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := r.Run(context.Background()); err != nil || state != StateDone {
		t.Fatalf("run=%s %v\n%s", state, err, out.String())
	}
	opts.Resume = r.Delivery()
	before := len(readJournal(t, f, r.Delivery()))
	missing := opts
	missing.ApprovedSnapshot = nil
	if wrong, err := Resume(context.Background(), missing); !errors.Is(err, ErrSnapshotRequired) {
		if wrong != nil {
			wrong.Release()
		}
		t.Fatalf("missing review receipt=%v", err)
	}
	if err := os.Remove(filepath.Join(f.root, routing.PlanPath("greetings"))); err != nil {
		t.Fatal(err)
	}
	opts.Supervisor = &SuperviseOptions{}
	resumed, err := Resume(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Release()
	if resumed.terminal != StateDone || resumed.plan.Context != "Original context." {
		t.Fatalf("review recovery=%s %s", resumed.terminal, resumed.plan.Context)
	}
	if len(readJournal(t, f, r.Delivery())) != before {
		t.Fatal("readonly review recovery changed journal")
	}
}

func TestApprovedSnapshotSupervisionReviewUsesExactApprovedSpec(t *testing.T) {
	t.Parallel()
	f, snapshot := approvedSnapshotFixture(t)
	snapshot.Content = bytes.ReplaceAll(snapshot.Content, []byte("Original context."), []byte("Approved context outside Git."))
	snapshot.ContentDigest = snapshotTestDigest(snapshot.Content)
	var out bytes.Buffer
	opts := snapshotTestOptions(f, snapshot, "default", &out)
	opts.Supervision = true
	r, err := New(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := r.Run(context.Background()); err != nil || state != StateDone {
		t.Fatalf("run=%s %v\n%s", state, err, out.String())
	}
	observer := SupervisionOptions{Workspace: f.root, Delivery: r.Delivery()}
	launches := 0
	review := SupervisionReviewOptions{Executable: "synthetic-review", ApprovedSnapshot: snapshot, Runner: commandRunnerFunc(func(ctx context.Context, c publication.Command) (publication.CommandResult, error) {
		if len(c.Args) == 1 && c.Args[0] == "capabilities" {
			return publication.CommandResult{Stdout: []byte(`{"commands":["review"]}`)}, nil
		}
		if len(c.Args) == 2 && c.Args[1] == "-h" {
			return publication.CommandResult{Stderr: []byte(" -base string\n -spec string\n -full\n -out string\n")}, nil
		}
		launches++
		if len(c.Args) != 8 {
			return publication.CommandResult{}, fmt.Errorf("unexpected review argv: %v", c.Args)
		}
		body, err := os.ReadFile(c.Args[4])
		if err != nil || !bytes.Equal(body, snapshot.Content) {
			return publication.CommandResult{}, fmt.Errorf("review spec differs from approved bytes: %q %v", body, err)
		}
		if err := os.MkdirAll(c.Args[7], 0700); err != nil {
			return publication.CommandResult{}, err
		}
		return publication.CommandResult{}, writeSupervisionReviewEvidenceError(c, "SHIP", true)
	})}
	missing := review
	missing.ApprovedSnapshot = nil
	if _, err := RunSupervisionReview(context.Background(), observer, missing); !errors.Is(err, ErrSnapshotRequired) {
		t.Fatalf("missing review snapshot=%v", err)
	}
	wrong := *snapshot
	wrong.ReceiptID = "foreign"
	invalid := review
	invalid.ApprovedSnapshot = &wrong
	if _, err := RunSupervisionReview(context.Background(), observer, invalid); !errors.Is(err, ErrSnapshotMismatch) {
		t.Fatalf("wrong review receipt=%v", err)
	}
	if launches != 0 {
		t.Fatal("review launched without approved pair")
	}
	source := filepath.Join(f.root, routing.PlanPath("greetings"))
	external := []byte("new unapproved edit\r\n")
	if err := os.WriteFile(source, external, 0640); err != nil {
		t.Fatal(err)
	}
	job, err := RunSupervisionReview(context.Background(), observer, review)
	if err != nil || job == nil || job.State != "reported" || job.Outcome != "SHIP" || job.SpecContentDigest != snapshot.ContentDigest || launches != 1 {
		t.Fatalf("review=%+v %v launches=%d", job, err, launches)
	}
	alias := filepath.Join(t.TempDir(), "workspace-alias")
	if runtime.GOOS == "windows" {
		alias = strings.ToLower(f.root[:1]) + f.root[1:]
	} else if err := os.Symlink(f.root, alias); err != nil {
		t.Fatal(err)
	}
	observer.Workspace = alias
	aliased, err := RunSupervisionReview(context.Background(), observer, review)
	if err != nil || aliased == nil || aliased.ID != job.ID || aliased.Outcome != "SHIP" || launches != 1 {
		t.Fatalf("same workspace alias refused or launched again: job=%+v error=%v launches=%d", aliased, err, launches)
	}
	body, err := os.ReadFile(source)
	if err != nil || !bytes.Equal(body, external) {
		t.Fatal("review changed original source", err)
	}
}

func TestApprovedSnapshotDoesNotRequireEditableSource(t *testing.T) {
	t.Parallel()
	f, snapshot := approvedSnapshotFixture(t)
	f.run(t, "rm", routing.PlanPath("greetings"))
	f.run(t, "commit", "-qm", "test: remove editable plan")
	var out bytes.Buffer
	r, err := New(context.Background(), snapshotTestOptions(f, snapshot, "default", &out))
	if err != nil {
		t.Fatal(err)
	}
	if state, err := r.Run(context.Background()); err != nil || state != StateDone {
		t.Fatalf("run=%s %v\n%s", state, err, out.String())
	}
	if _, err := os.Stat(filepath.Join(f.root, routing.PlanPath("greetings"))); !os.IsNotExist(err) {
		t.Fatal("source recreated", err)
	}
}

func TestApprovedSnapshotCancelAndResumePreservesSource(t *testing.T) {
	t.Parallel()
	f, snapshot := approvedSnapshotFixture(t)
	var out bytes.Buffer
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opts := snapshotTestOptions(f, snapshot, "default", &out)
	opts.Runner = commandRunnerFunc(func(ctx context.Context, c publication.Command) (publication.CommandResult, error) {
		if c.Executable == f.fake {
			for _, arg := range c.Args {
				if arg == "run" {
					cancel()
					break
				}
			}
		}
		return (publication.ExecRunner{}).Run(ctx, c)
	})
	r, err := New(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(f.root, routing.PlanPath("greetings"))
	external := []byte(strings.Replace(approvedSnapshotSource, "Original context.", "External proposal.", 1))
	if err := os.WriteFile(source, external, 0640); err != nil {
		t.Fatal(err)
	}
	if state, err := r.Run(runCtx); state != StateCanceled || err != nil {
		t.Fatalf("cancel=%s %v\n%s", state, err, out.String())
	}
	body, err := os.ReadFile(source)
	if err != nil || !bytes.Equal(body, external) {
		t.Fatal("cancel changed source", err)
	}
	resume := snapshotTestOptions(f, snapshot, "default", &out)
	resume.Resume = r.Delivery()
	resumed, err := Resume(context.Background(), resume)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.plan.Context != "Original context." {
		resumed.Release()
		t.Fatal("cancel resume changed context")
	}
	if state, err := resumed.Run(context.Background()); err != nil || state != StateDone {
		t.Fatalf("resume=%s %v\n%s", state, err, out.String())
	}
	body, err = os.ReadFile(source)
	if err != nil || !bytes.Equal(body, external) {
		t.Fatal("cancel resume changed source", err)
	}
}
