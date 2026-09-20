package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/batuta-ai/core/judge"
)

// judgeEnvVar selects another judge config file or turns the judge off;
// Config.Judge reads the API key from the env var named by key_env.
const judgeEnvVar = "BATUTA_JUDGE"

func runJudge(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("a judge form is required; available forms: ask, probe")
	}
	switch args[0] {
	case "ask":
		return runJudgeAsk(args[1:], stdout, stderr)
	case "probe":
		return runJudgeProbe(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown judge form %q; available forms: ask, probe", args[0])
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
