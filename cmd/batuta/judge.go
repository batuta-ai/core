package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/batuta-ai/core/gates"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/judge"
	"github.com/batuta-ai/core/loop"
)

// judgeEnvVar selects another judge config file or turns the judge off;
// Config.Judge reads the API key from the env var named by key_env.
const judgeEnvVar = "BATUTA_JUDGE"

func runJudge(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("a judge form is required; available forms: ask, probe, replay")
	}
	switch args[0] {
	case "ask":
		return runJudgeAsk(args[1:], stdout, stderr)
	case "probe":
		return runJudgeProbe(args[1:], stdout, stderr)
	case "replay":
		return runJudgeReplay(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown judge form %q; available forms: ask, probe, replay", args[0])
	}
}

func runJudgeAsk(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("judge ask", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	stateFile := flags.String("state-file", "", "JSON file holding the bounded state")
	questionsFile := flags.String("questions-file", "", "JSON file with the questions map in the request shape")
	decision := flags.String("decision", "manual", "decision name carried into the trace records")
	configPath := flags.String("config", "", "judge config path (default: .batuta/judge.json under --workspace)")
	workspace := flags.String("workspace", "", "workspace directory (default: current directory)")
	baseURL := flags.String("base-url", "", "override the configured provider base URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *stateFile == "" || *questionsFile == "" {
		return errors.New("usage: batuta judge ask --state-file <path> --questions-file <path> [--decision <name>] [--config <path>] [--workspace <dir>] [--base-url <url>]")
	}

	state, err := readJSONFile(*stateFile)
	if err != nil {
		return fmt.Errorf("judge ask: state file: %w", err)
	}
	questionsPayload, err := readJSONFile(*questionsFile)
	if err != nil {
		return fmt.Errorf("judge ask: questions file: %w", err)
	}
	var questions map[string]judge.Question
	if err := json.Unmarshal(questionsPayload, &questions); err != nil {
		return fmt.Errorf("judge ask: questions file %s must be a JSON object in the request questions shape: %w", *questionsFile, err)
	}
	if questions == nil {
		return fmt.Errorf("judge ask: questions file %s must be a JSON object in the request questions shape", *questionsFile)
	}

	j, err := buildJudge(*configPath, *workspace, *baseURL)
	if err != nil {
		return judgeFailure(stderr, err)
	}
	response, err := j.Ask(context.Background(), judge.Request{Decision: *decision, State: json.RawMessage(state), Questions: questions})
	if err != nil {
		return judgeFailure(stderr, err)
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(response)
}

func runJudgeProbe(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("judge probe", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "judge config path (default: .batuta/judge.json under --workspace)")
	workspace := flags.String("workspace", "", "workspace directory (default: current directory)")
	baseURL := flags.String("base-url", "", "override the configured provider base URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: batuta judge probe [--config <path>] [--workspace <dir>] [--base-url <url>]")
	}

	j, err := buildJudge(*configPath, *workspace, *baseURL)
	if err != nil {
		return judgeFailure(stderr, err)
	}
	response, err := j.Ask(context.Background(), judge.Request{
		Decision: "probe",
		State:    "connection check",
		Questions: map[string]judge.Question{
			"ok": {Type: judge.QuestionNoul, Instructions: "The state says the connection works."},
		},
	})
	if err != nil {
		return judgeFailure(stderr, err)
	}
	if provider, ok := j.(interface{ LastProvider() judge.Provider }); ok {
		if name := provider.LastProvider(); name != "" {
			fmt.Fprintf(stdout, "provider: %s\n", name)
		}
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(response)
}

// loadJudgeConfig resolves the config: an explicit --config path wins over
// BATUTA_JUDGE, and the default stays .batuta/judge.json under the workspace.
func loadJudgeConfig(configPath, workspace string) (judge.Config, error) {
	root, err := workspaceRoot(workspace)
	if err != nil {
		return judge.Config{}, err
	}
	getenv := func(name string) string {
		if name == judgeEnvVar && configPath != "" {
			return configPath
		}
		return os.Getenv(name)
	}
	return judge.LoadConfig(root, getenv)
}

func buildJudge(configPath, workspace, baseURL string) (judge.Judge, error) {
	config, err := loadJudgeConfig(configPath, workspace)
	if err != nil {
		return nil, err
	}
	if baseURL != "" {
		config.BaseURL = baseURL
	}
	return config.Judge(os.Getenv)
}

// judgeFailure separates the two failure classes: an unavailable judge is the
// fail-closed outcome on stderr with exit 2 and keeps today's deterministic
// rule, while a config error is a plain error and exits 1.
// readJournal decodes a delivery journal read-only: the file is opened for
// reading and decoded directly, never through the append paths of
// journal.Store.
func readJournal(path string) ([]journal.Record, error) {
	handle, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer handle.Close()
	return journal.Decode(handle)
}

type replayOpenedDetail struct {
	Slug  string              `json:"slug"`
	Tasks []replayTaskSummary `json:"tasks"`
}

type replayTaskSummary struct {
	ID    string `json:"task_id"`
	Title string `json:"title"`
}

type replayFinishedDetail struct {
	Execution   int  `json:"execution"`
	TreeChanged bool `json:"tree_changed"`
}

type replayFailureDetail struct {
	Execution int    `json:"execution"`
	Blocker   string `json:"blocker"`
}

type replayExecutionDetail struct {
	Execution int `json:"execution"`
}

type replayAttemptKey struct {
	taskID    string
	execution int
}

type replayAttempt struct {
	taskID      string
	execution   int
	treeChanged bool
	report      gates.Report
	outcome     string
	at          time.Time
}

// replayAttempts pairs every gates_reported record with its matching
// executor_finished record and the first outcome record that follows it.
// It also returns the delivery slug and the task titles from the
// delivery_opened record.
func replayAttempts(records []journal.Record) (attempts []replayAttempt, titles map[string]string, slug string) {
	type finishedKey struct {
		taskID    string
		execution int
	}
	finished := map[finishedKey]journal.Record{}
	outcomes := map[replayAttemptKey]string{}
	titles = map[string]string{}
	for _, record := range records {
		switch record.Kind {
		case loop.KindOpened:
			var detail replayOpenedDetail
			if json.Unmarshal(record.Detail, &detail) == nil {
				slug = detail.Slug
				for _, task := range detail.Tasks {
					titles[task.ID] = task.Title
				}
			}
		case loop.KindFinished:
			var detail replayFinishedDetail
			if json.Unmarshal(record.Detail, &detail) == nil {
				finished[finishedKey{record.TaskID, detail.Execution}] = record
			}
		case loop.KindCandidate:
			var detail replayExecutionDetail
			if json.Unmarshal(record.Detail, &detail) == nil {
				key := replayAttemptKey{record.TaskID, detail.Execution}
				if _, seen := outcomes[key]; !seen {
					outcomes[key] = "candidate"
				}
			}
		case loop.KindQuestion:
			var detail replayExecutionDetail
			if json.Unmarshal(record.Detail, &detail) == nil {
				key := replayAttemptKey{record.TaskID, detail.Execution}
				if _, seen := outcomes[key]; !seen {
					outcomes[key] = "question"
				}
			}
		case loop.KindFailure:
			var detail replayFailureDetail
			if json.Unmarshal(record.Detail, &detail) == nil {
				key := replayAttemptKey{record.TaskID, detail.Execution}
				if _, seen := outcomes[key]; !seen {
					if detail.Blocker == "" {
						detail.Blocker = "failure"
					}
					outcomes[key] = detail.Blocker
				}
			}
		}
	}
	seen := map[replayAttemptKey]bool{}
	for _, record := range records {
		if record.Kind != loop.KindGates {
			continue
		}
		var report gates.Report
		if err := json.Unmarshal(record.Detail, &report); err != nil {
			continue
		}
		key := replayAttemptKey{record.TaskID, report.Execution}
		finishedRecord, ok := finished[finishedKey{record.TaskID, report.Execution}]
		if !ok || seen[key] {
			continue
		}
		seen[key] = true
		treeChanged := false
		var finishedDetail replayFinishedDetail
		if json.Unmarshal(finishedRecord.Detail, &finishedDetail) == nil {
			treeChanged = finishedDetail.TreeChanged
		}
		attempts = append(attempts, replayAttempt{
			taskID: record.TaskID, execution: report.Execution, treeChanged: treeChanged,
			report: report, outcome: outcomes[key], at: record.At,
		})
	}
	return attempts, titles, slug
}

func runJudgeReplay(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("judge replay", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	journalPath := flags.String("journal", "", "delivery journal to replay (.batuta/journal/<delivery>.jsonl)")
	runs := flags.String("runs", "", "run-log directory (default: <workspace>/.batuta/runs)")
	configPath := flags.String("config", "", "judge config path (default: .batuta/judge.json under --workspace)")
	decision := flags.String("decision", "claim_evidence", "decision name asked and printed")
	workspace := flags.String("workspace", "", "workspace directory (default: current directory)")
	baseURL := flags.String("base-url", "", "override the configured provider base URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *journalPath == "" {
		return errors.New("usage: batuta judge replay --journal <path> [--runs <dir>] [--config <path>] [--decision <name>] [--workspace <dir>] [--base-url <url>]")
	}

	root, err := workspaceRoot(*workspace)
	if err != nil {
		return err
	}
	records, err := readJournal(*journalPath)
	if err != nil {
		return fmt.Errorf("judge replay: %s: %w", *journalPath, err)
	}
	config, err := loadJudgeConfig(*configPath, *workspace)
	if err != nil {
		return err
	}
	if *baseURL != "" {
		config.BaseURL = *baseURL
	}
	j, err := config.Judge(os.Getenv)
	if err != nil {
		return judgeFailure(stderr, err)
	}
	provider := judgeProviderName(j, config)

	attempts, titles, slug := replayAttempts(records)
	delivery := strings.TrimSuffix(filepath.Base(*journalPath), ".jsonl")
	maxBytes := config.MaxStateBytes
	if maxBytes <= 0 {
		maxBytes = 100000
	}
	answered := 0
	for _, attempt := range attempts {
		logPath := attempt.runLogPath(root, *runs, delivery, slug)
		log, err := os.ReadFile(logPath)
		if err != nil {
			fmt.Fprintf(stdout, "%s e%d skipped %s\n", attempt.taskID, attempt.execution, logPath)
			continue
		}
		state, err := loop.ReplayClaimEvidenceState(loop.ReplayClaimEvidenceInput{
			Workspace: root, TaskID: attempt.taskID, TaskTitle: titles[attempt.taskID],
			Report: attempt.report, TreeChanged: attempt.treeChanged, RunLog: string(log),
		}, maxBytes)
		if err != nil {
			return fmt.Errorf("judge replay: %s e%d: %w", attempt.taskID, attempt.execution, err)
		}
		response, err := j.Ask(context.Background(), judge.Request{
			Decision: *decision, State: state, Questions: loop.ClaimEvidenceQuestions(),
		})
		if err != nil {
			if answered == 0 {
				return judgeFailure(stderr, err)
			}
			fmt.Fprintf(stdout, "%s e%d outcome=%s unavailable=%s\n", attempt.taskID, attempt.execution, attempt.outcome, judgeReplayReason(err))
			continue
		}
		answered++
		provider = judgeProviderName(j, config)
		fmt.Fprintf(stdout, "%s e%d outcome=%s claim_unsupported=%.2f verifier_contradicted=%.2f provider=%s\n",
			attempt.taskID, attempt.execution, attempt.outcome,
			response.Answers["claim_unsupported"].Noul, response.Answers["verifier_contradicted"].Noul, provider)
	}
	return nil
}

// judgeProviderName reports the provider that answered the last ask; a
// single-provider judge falls back to the configured provider.
func judgeProviderName(j judge.Judge, config judge.Config) string {
	if named, ok := j.(interface {
		LastProvider() judge.Provider
	}); ok {
		if name := named.LastProvider(); name != "" {
			return string(name)
		}
	}
	return string(config.Provider)
}

// judgeReplayReason keeps the typed unavailability reason when there is one.
func judgeReplayReason(err error) string {
	var unavailable *judge.UnavailableError
	if errors.As(err, &unavailable) {
		return unavailable.Reason
	}
	return err.Error()
}

func (a replayAttempt) runLogPath(workspace, runs, delivery, slug string) string {
	if runs == "" {
		runs = filepath.Join(workspace, ".batuta", "runs")
	} else if !filepath.IsAbs(runs) {
		runs = filepath.Join(workspace, runs)
	}
	return filepath.Join(runs, loop.ReplayRunLogName(delivery, slug, a.taskID, a.at, a.execution))
}

func judgeFailure(stderr io.Writer, err error) error {
	var unavailable *judge.UnavailableError
	if !errors.As(err, &unavailable) {
		return err
	}
	fmt.Fprintln(stderr, "judge:", err)
	return &ExitError{Code: 2, State: "unavailable"}
}

func readJSONFile(path string) (json.RawMessage, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if !json.Valid(payload) {
		return nil, fmt.Errorf("%s is not valid JSON", path)
	}
	return json.RawMessage(payload), nil
}
