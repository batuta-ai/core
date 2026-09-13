package loop

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/publication"
	"github.com/batuta-ai/core/routing"
)

// SupervisionReviewJob is evidence for a separate acceptance stage. Reported
// means the engine returned artifacts, not that a conductor approved delivery.
type SupervisionReviewJob struct {
	Outcome           string            `json:"outcome,omitempty"`
	Acceptance        string            `json:"acceptance"`
	ID                string            `json:"id,omitempty"`
	Delivery          string            `json:"delivery"`
	FinalCommit       string            `json:"final_commit,omitempty"`
	Base              string            `json:"base,omitempty"`
	SpecDigest        string            `json:"spec_digest,omitempty"`
	SpecPath          string            `json:"spec_path,omitempty"`
	Slug              string            `json:"slug,omitempty"`
	State             string            `json:"state"`
	Reason            string            `json:"reason,omitempty"`
	Snapshot          string            `json:"snapshot,omitempty"`
	Spec              string            `json:"spec,omitempty"`
	SpecContentDigest string            `json:"spec_content_digest,omitempty"`
	Artifacts         string            `json:"artifacts,omitempty"`
	ArtifactDigests   map[string]string `json:"artifact_digests,omitempty"`
	Attempts          int               `json:"attempts"`
	LaunchedAt        time.Time         `json:"launched_at,omitempty"`
	FinishedAt        time.Time         `json:"finished_at,omitempty"`
	ExitCode          int               `json:"exit_code,omitempty"`
}

// The same core executable probes its engine before using the routed readonly
// adapters and independent proof checks in `review`. No fallback is installed.
type SupervisionReviewOptions struct {
	Executable string
	Runner     publication.CommandRunner
	Timeout    time.Duration
}

var supervisionCommit = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

func supervisionReviewCandidate(delivery string, records []journal.Record) *SupervisionReviewJob {
	if len(records) < 2 || records[0].Kind != KindOpened || records[len(records)-1].Kind != KindTerminal {
		return nil
	}
	var terminal terminalDetail
	if json.Unmarshal(records[len(records)-1].Detail, &terminal) != nil || terminal.State != StateDone || terminal.CleanupPending || terminal.BookkeepingPending || len(terminal.Deletions) > 0 {
		return nil
	}
	var opened openedDetail
	job := &SupervisionReviewJob{Delivery: delivery, State: "pending", Acceptance: "pending"}
	if json.Unmarshal(records[0].Detail, &opened) != nil {
		job.Reason = "delivery opening identity is unavailable"
		return job
	}
	job.FinalCommit, job.Base, job.SpecDigest = terminal.FinalCommit, opened.Head, opened.PlanDigest
	job.SpecPath, job.Slug = opened.PlanPath, opened.Slug
	if !supervisionCommit.MatchString(job.FinalCommit) || !supervisionCommit.MatchString(job.Base) || len(job.SpecDigest) != 64 || job.SpecPath == "" || job.Slug == "" {
		job.Reason = "immutable final commit, original base or spec identity is unknown"
		return job
	}
	identity, _ := json.Marshal([]string{delivery, job.FinalCommit, job.Base, job.SpecDigest})
	job.ID = fmt.Sprintf("%x", sha256.Sum256(identity))
	return job
}

func supervisionReviewDirectory(opts SupervisionOptions, job SupervisionReviewJob) string {
	return filepath.Join(opts.Workspace, ".batuta", "reviews", "supervision", job.ID)
}

// RunSupervisionReview serializes a logical job across cursors and supervisors.
// A durable launch intent survives loss of the supervisor. Without a trustworthy
// result receipt, recovery stops at uncertain instead of starting another paid
// process: neither a stale heartbeat nor a dead parent proves the child exited.
func RunSupervisionReview(ctx context.Context, observer SupervisionOptions, opts SupervisionReviewOptions) (*SupervisionReviewJob, error) {
	observer, err := normalizeSupervisionOptions(observer)
	if err != nil {
		return nil, err
	}
	observation, err := ObserveSupervision(observer)
	if err != nil {
		return nil, err
	}
	job := observation.Review
	if job == nil || job.ID == "" {
		return job, nil
	}
	directory := supervisionReviewDirectory(observer, *job)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	release, err := guardPresence(filepath.Join(directory, "ownership"))
	if err != nil {
		return nil, err
	}
	defer release()
	statePath := filepath.Join(directory, "job.json")
	data, err := readSupervisionFile(statePath, 1<<20)
	if err == nil {
		var saved SupervisionReviewJob
		if err := json.Unmarshal(data, &saved); err != nil {
			return nil, err
		}
		if saved.ID != job.ID || saved.Delivery != job.Delivery || saved.FinalCommit != job.FinalCommit || saved.Base != job.Base || saved.SpecDigest != job.SpecDigest || saved.SpecPath != job.SpecPath || saved.Slug != job.Slug {
			return nil, errors.New("loop: review job identity mismatch")
		}
		job = &saved
		job.Acceptance = "pending"
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	persist := func() (*SupervisionReviewJob, error) { return job, writeSupervisionJSON(statePath, job) }
	fail := func(err error) (*SupervisionReviewJob, error) {
		job.State = "failed"
		job.Outcome, job.Acceptance = "execution_failed", "pending"
		job.Reason = err.Error()
		job.FinishedAt = observer.Now()
		return persist()
	}
	if job.State == "launching" {
		job.State, job.Reason = "uncertain", "review launch was interrupted; reconcile reviewer execution before any further attempt"
		return persist()
	}
	switch job.State {
	case "reported":
		if err := validateSupervisionReviewSnapshot(ctx, job); err != nil {
			return fail(err)
		}
		if err := supervisionReviewArtifacts(job, false); err != nil {
			return fail(err)
		}
		if err := classifySupervisionReview(job); err != nil {
			return fail(err)
		}
		return persist()
	case "failed", "uncertain":
		return job, nil
	case "pending":
	default:
		return nil, errors.New("loop: unknown review job state")
	}
	if active, err := supervisionReviewActiveRunner(observer); err != nil || active {
		job.Reason = "runner ownership prevents review"
		if err != nil {
			job.Reason = err.Error()
		}
		return persist()
	}

	if opts.Runner == nil {
		opts.Runner = publication.ExecRunner{}
	}
	if opts.Timeout == 0 {
		opts.Timeout = time.Hour
	}
	if opts.Timeout < 0 {
		return nil, errors.New("loop: review timeout must be positive")
	}
	reviewCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	if err := probeSupervisionReview(reviewCtx, opts, observer.Workspace); err != nil {
		return fail(err)
	}
	if job.Attempts != 0 {
		job.State = "uncertain"
		job.Reason = "review attempt already recorded"
		return persist()
	}
	// A fresh private clone has independent refs, index, objects and review state;
	// subsequent deliveries cannot change the source or reuse incremental coverage.
	job.Snapshot = filepath.Join(directory, "source")
	job.Spec = filepath.Join(directory, job.Slug+".md")
	job.Artifacts = filepath.Join(directory, "artifacts")
	if err := prepareSupervisionReviewSnapshot(reviewCtx, observer, job); err != nil {
		return fail(err)
	}
	if active, err := supervisionReviewActiveRunner(observer); err != nil || active {
		return fail(errors.Join(errors.New("loop: runner became active before review launch"), err))
	}
	job.State, job.Reason = "launching", ""
	job.Attempts++
	job.LaunchedAt = observer.Now()
	if _, err := persist(); err != nil {
		return nil, err
	}
	result, runErr := opts.Runner.Run(reviewCtx, publication.Command{Executable: opts.Executable, Directory: job.Snapshot, Args: []string{"review", "--base", job.Base, "--spec", job.Spec, "--full", "--out", job.Artifacts}})
	job.ExitCode = result.ExitCode
	var exitErr *exec.ExitError
	verdictExit := (result.ExitCode == 2 || result.ExitCode == 3) && errors.As(runErr, &exitErr)
	if (runErr != nil && !verdictExit) || reviewCtx.Err() != nil || result.StdoutTruncated || result.StderrTruncated {
		return fail(errors.Join(errors.New("loop: review engine execution failed"), runErr, reviewCtx.Err()))
	}
	if result.ExitCode != 0 && result.ExitCode != 2 && result.ExitCode != 3 {
		return fail(fmt.Errorf("loop: review engine exited %d", result.ExitCode))
	}
	if err := validateSupervisionReviewSnapshot(reviewCtx, job); err != nil {
		return fail(err)
	}
	records, err := readSupervisionRecords(observer)
	if err != nil {
		return fail(err)
	}
	candidate := supervisionReviewCandidate(observer.Delivery, records)
	if candidate == nil || candidate.ID != job.ID {
		return fail(errors.New("loop: delivered identity changed during review"))
	}
	if active, err := supervisionReviewActiveRunner(observer); err != nil || active {
		return fail(errors.Join(errors.New("loop: runner ownership changed during review"), err))
	}
	if err := supervisionReviewArtifacts(job, true); err != nil {
		return fail(err)
	}
	if err := classifySupervisionReview(job); err != nil {
		return fail(err)
	}
	job.State, job.Reason = "reported", "review evidence awaits conductor judgment"
	job.FinishedAt = observer.Now()
	return persist()
}

func supervisionReviewActiveRunner(opts SupervisionOptions) (bool, error) {
	entries, err := os.ReadDir(filepath.Join(opts.Workspace, journal.Dir))
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".lock") {
			continue
		}
		owner, err := liveDeliveryOwner(opts.Workspace, strings.TrimSuffix(entry.Name(), ".lock"), opts.Now())
		if err != nil {
			return false, err
		}
		if owner != nil {
			return true, nil
		}
	}
	return false, nil
}

func probeSupervisionReview(ctx context.Context, opts SupervisionReviewOptions, root string) error {
	if opts.Executable == "" {
		return errors.New("loop: review engine is unavailable")
	}
	result, err := opts.Runner.Run(ctx, publication.Command{Executable: opts.Executable, Directory: root, Args: []string{"capabilities"}})
	var capabilities struct {
		Commands []string `json:"commands"`
	}
	if err != nil || result.ExitCode != 0 || result.StdoutTruncated || json.Unmarshal(result.Stdout, &capabilities) != nil || !slices.Contains(capabilities.Commands, "review") {
		return errors.Join(errors.New("loop: core review engine is unavailable"), err)
	}
	result, err = opts.Runner.Run(ctx, publication.Command{Executable: opts.Executable, Directory: root, Args: []string{"review", "-h"}})
	// FlagSet prints help and may return a nonzero exit. Inspect the supported
	// flag declarations as well as the machine-readable command capability.
	help := string(result.Stdout) + string(result.Stderr)
	if result.StdoutTruncated || result.StderrTruncated {
		return errors.New("loop: review capability help was truncated")
	}
	for _, flag := range []string{"base", "spec", "full", "out"} {
		if !regexp.MustCompile(`(?m)^\s+-` + flag + `(?:\s|$)`).MatchString(help) {
			return errors.Join(fmt.Errorf("loop: review engine lacks --%s", flag), err)
		}
	}
	return nil
}

func supervisionReviewGit(ctx context.Context, root string, args ...string) ([]byte, error) {
	git, err := exec.LookPath("git")
	if err != nil {
		return nil, err
	}
	result, err := (publication.ExecRunner{}).Run(ctx, publication.Command{Executable: git, Directory: root, Args: args})
	if err != nil || result.ExitCode != 0 || result.StdoutTruncated || result.StderrTruncated {
		return nil, errors.Join(fmt.Errorf("loop: review snapshot git %s failed: %s", args[0], strings.TrimSpace(string(result.Stderr))), err)
	}
	return result.Stdout, nil
}

func prepareSupervisionReviewSnapshot(ctx context.Context, opts SupervisionOptions, job *SupervisionReviewJob) error {
	// An interrupted preparation has no launch intent and can be rebuilt. Never
	// remove a snapshot after launch; its evidence belongs to that attempt.
	if err := os.RemoveAll(job.Snapshot); err != nil {
		return err
	}
	if _, err := supervisionReviewGit(ctx, opts.Workspace, "clone", "--no-hardlinks", "--no-checkout", "--", opts.Workspace, job.Snapshot); err != nil {
		return err
	}
	if _, err := supervisionReviewGit(ctx, job.Snapshot, "checkout", "--detach", job.FinalCommit, "--"); err != nil {
		return err
	}
	if _, err := supervisionReviewGit(ctx, job.Snapshot, "merge-base", "--is-ancestor", job.Base, job.FinalCommit); err != nil {
		return err
	}
	// Resolve the original contract from the original base first. Archived paths
	// are fallback candidates only when their parsed contract digest still matches.
	for _, source := range []struct{ commit, path string }{{job.Base, job.SpecPath}, {job.FinalCommit, job.SpecPath}, {job.FinalCommit, ".batuta/plans/done/" + job.Slug + ".md"}} {
		data, err := supervisionReviewGit(ctx, job.Snapshot, "show", source.commit+":"+source.path)
		if err != nil {
			continue
		}
		plan, err := routing.ParsePlan(job.Slug, data)
		if err != nil || plan.Set.Digest != job.SpecDigest {
			continue
		}
		if err := os.WriteFile(job.Spec, data, 0600); err != nil {
			return err
		}
		job.SpecContentDigest = fmt.Sprintf("%x", sha256.Sum256(data))
		break
	}
	if job.SpecContentDigest == "" {
		return errors.New("loop: exact delivered spec digest cannot be resolved")
	}
	exclude := filepath.Join(job.Snapshot, ".git", "info", "exclude")
	file, err := os.OpenFile(exclude, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.WriteString("\n/.batuta/reviews/state/\n")
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return err
	}
	return validateSupervisionReviewSnapshot(ctx, job)
}

func validateSupervisionReviewSnapshot(ctx context.Context, job *SupervisionReviewJob) error {
	head, err := supervisionReviewGit(ctx, job.Snapshot, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(head)) != job.FinalCommit {
		return errors.New("loop: reviewed snapshot HEAD changed")
	}
	status, err := supervisionReviewGit(ctx, job.Snapshot, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return err
	}
	if len(status) != 0 {
		return errors.New("loop: reviewed snapshot source changed")
	}
	spec, err := readSupervisionFile(job.Spec, 32<<20)
	if err != nil {
		return err
	}
	if fmt.Sprintf("%x", sha256.Sum256(spec)) != job.SpecContentDigest {
		return errors.New("loop: reviewed spec changed")
	}
	return nil
}

func supervisionReviewArtifacts(job *SupervisionReviewJob, record bool) error {
	if record {
		job.ArtifactDigests = make(map[string]string)
	}
	for _, name := range []string{"manifest.json", "findings.json", "review.md", "state.json"} {
		data, err := readSupervisionFile(filepath.Join(job.Artifacts, name), 32<<20)
		if err != nil || len(data) == 0 {
			return errors.Join(fmt.Errorf("loop: review artifact %s unavailable", name), err)
		}
		digest := fmt.Sprintf("%x", sha256.Sum256(data))
		if record {
			job.ArtifactDigests[name] = digest
		} else if job.ArtifactDigests[name] != digest {
			return fmt.Errorf("loop: review artifact %s changed", name)
		}
	}
	return nil
}

// The engine publishes a checkpoint and a canonical walkthrough, not a JSON
// Report. Require both coverage signals; exit 3 alone also means incomplete work.
func classifySupervisionReview(job *SupervisionReviewJob) error {
	read := func(name string, value any) error {
		data, err := readSupervisionFile(filepath.Join(job.Artifacts, name), 32<<20)
		if err != nil {
			return err
		}
		return json.Unmarshal(data, value)
	}
	var manifest struct {
		Base    string
		Cohorts []json.RawMessage
		Files   []struct{ Selected, Ignored bool }
	}
	var state struct {
		Head    string
		Pending []json.RawMessage
	}
	var findings []json.RawMessage
	if err := errors.Join(read("manifest.json", &manifest), read("state.json", &state), read("findings.json", &findings)); err != nil {
		return fmt.Errorf("loop: malformed review evidence: %w", err)
	}
	if manifest.Base != job.Base || (state.Head != job.Base && state.Head != job.FinalCommit) {
		return errors.New("loop: review evidence identity mismatch")
	}
	data, err := readSupervisionFile(filepath.Join(job.Artifacts, "review.md"), 32<<20)
	if err != nil {
		return err
	}
	report := string(data)
	verdict := regexp.MustCompile(`(?m)^Verdict: (SHIP|FIX_BEFORE_SHIP|REWORK)\n?\z`).FindStringSubmatch(report)
	coverage := regexp.MustCompile(`(?m)^Coverage: ([0-9]+)/([0-9]+) cohorts$`).FindAllStringSubmatch(report, -1)
	if len(verdict) != 2 || len(coverage) != 1 || !strings.HasPrefix(report, "Review walkthrough\n\nBase: "+job.Base+"\n") || !strings.Contains(report, "\nCriteria:\n| Criterion | Status | Evidence |\n") {
		return errors.New("loop: review verdict or coverage evidence is unavailable")
	}
	expectedExit := map[string]int{"SHIP": 0, "FIX_BEFORE_SHIP": 2, "REWORK": 3}[verdict[1]]
	if job.ExitCode != expectedExit {
		return errors.New("loop: review verdict and exit status disagree")
	}
	covered, err := strconv.Atoi(coverage[0][1])
	if err != nil {
		return err
	}
	total, err := strconv.Atoi(coverage[0][2])
	if err != nil {
		return err
	}
	if total != len(manifest.Cohorts) || covered > total {
		return errors.New("loop: inconsistent review coverage")
	}
	complete := covered == total && state.Head == job.FinalCommit && len(state.Pending) == 0 && !strings.Contains(report, "\nSpec coverage: uncovered")
	for _, file := range manifest.Files {
		complete = complete && (file.Selected || file.Ignored)
	}
	job.Outcome, job.Acceptance = verdict[1], "pending"
	if !complete {
		job.Outcome = "incomplete_coverage"
	}
	return nil
}

func loadSupervisionReview(opts SupervisionOptions, candidate *SupervisionReviewJob) (*SupervisionReviewJob, error) {
	if candidate == nil || candidate.ID == "" {
		return candidate, nil
	}
	data, err := readSupervisionFile(filepath.Join(supervisionReviewDirectory(opts, *candidate), "job.json"), 1<<20)
	if errors.Is(err, os.ErrNotExist) {
		return candidate, nil
	}
	if err != nil {
		return nil, err
	}
	var saved SupervisionReviewJob
	if err := json.Unmarshal(data, &saved); err != nil {
		return nil, err
	}
	if saved.ID != candidate.ID || saved.Delivery != candidate.Delivery || saved.Base != candidate.Base || saved.FinalCommit != candidate.FinalCommit || saved.SpecDigest != candidate.SpecDigest || saved.SpecPath != candidate.SpecPath || saved.Slug != candidate.Slug {
		return nil, errors.New("loop: review job identity mismatch")
	}
	saved.Acceptance = "pending"
	return &saved, nil
}

func supervisionReviewEvent(opts SupervisionOptions, job *SupervisionReviewJob, sequence int) (*SupervisionEvent, error) {
	if job == nil || job.ID == "" || (job.State != "reported" && job.State != "failed" && job.State != "uncertain") {
		return nil, nil
	}
	data, err := json.Marshal(job)
	if err != nil {
		return nil, err
	}
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(data))
	// Keep the notified outcome available even if later validation invalidates
	// the mutable job. Different cursors publish identical content at this key.
	receipt := filepath.Join(supervisionReviewDirectory(opts, *job), "outcomes", digest[7:], "job.json")
	existing, err := readSupervisionFile(receipt, 1<<20)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(receipt), 0700); err != nil {
			return nil, err
		}
		if err := writeSupervisionJSON(receipt, job); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	} else if string(existing) != string(data) {
		return nil, errors.New("loop: review outcome receipt changed")
	}
	path, err := filepath.Rel(opts.Workspace, receipt)
	if err != nil {
		return nil, err
	}
	event := &SupervisionEvent{ID: opts.Delivery + ":review:" + job.ID + ":" + digest[7:], Delivery: opts.Delivery, Sequence: sequence, Kind: "review", At: job.FinishedAt, ReviewID: job.ID, ReviewOutcome: job.Outcome, ReviewState: job.State, Evidence: SupervisionEvidence{Path: filepath.ToSlash(path), Digest: digest}, Action: "Review evidence awaits conductor judgment; completion and SHIP grant no merge, publication or correction authority."}
	return event, nil
}
