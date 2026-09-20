package loop

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/core/executor"
	"github.com/batuta-ai/core/executor/acp"
	"github.com/batuta-ai/core/gates"
	"github.com/batuta-ai/core/publication"
	"github.com/batuta-ai/core/routing"
)

type unavailableBackend struct{}

func (unavailableBackend) Execute(context.Context, executor.Execution) (executor.Result, error) {
	return executor.Result{ExitCode: -1}, errors.New("task backend is unavailable")
}

func TestVerifierUsesIndependentBackend(t *testing.T) {
	t.Parallel()
	f := setup(t)
	var out bytes.Buffer
	r, err := New(context.Background(), f.options("unsigned-config", &out))
	if err != nil {
		t.Fatal(err)
	}
	r.backend = unavailableBackend{}
	adapter, err := r.adapterLocked("codex")
	if err != nil {
		t.Fatal(err)
	}
	ac := attemptContext{
		adapter: adapter, runtime: routing.RuntimeValue{Provider: "codex", Model: "fake-low"},
		worktree: attemptWorktree{Root: f.root}, base: f.base,
		plan: routing.PlanTask{TaskArtifact: routing.TaskArtifact{Title: "Add greeting one"}},
	}
	verdict, err := r.verify(context.Background(), &ac, gates.ParseCriteria([]string{"greeting is correct"}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !verdict.Pass || !strings.HasPrefix(verdict.Signal, "codex/fake-low: ") {
		t.Fatalf("verify() = %#v", verdict)
	}
	if ac.verifierDispatch.Executor != "codex" || ac.verifierDispatch.Model != "fake-low" {
		t.Fatalf("verifier dispatch = %#v", ac.verifierDispatch)
	}
	evidence, err := os.ReadFile(filepath.Join(f.state, "verifier-git-config"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(evidence), "command line:\tfalse") {
		t.Fatalf("verifier lost CLI environment: %s", evidence)
	}
}

func TestVerifierUsesResearchRowOfTaskLane(t *testing.T) {
	t.Parallel()
	roles := "\n| Role | Lane | Executor | Model | Cost |\n|---|---|---|---|---|\n" +
		"| research | low | claude | research-low | cents |\n" +
		"| research | medium | claude | research-medium | cents |\n" +
		"| research | high | claude | research-high | cents |\n"
	for _, lane := range []routing.Complexity{routing.ComplexityLow, routing.ComplexityMedium, routing.ComplexityHigh} {
		t.Run(string(lane), func(t *testing.T) {
			t.Parallel()
			assertVerifierSeat(t, lane, roles, "claude", "claude", "research-"+string(lane))
		})
	}
	t.Run("unseated lane uses nearest lower row", func(t *testing.T) {
		t.Parallel()
		assertVerifierSeat(t, routing.ComplexityHigh, strings.ReplaceAll(roles, "| research | high | claude | research-high | cents |\n", ""), "claude", "claude", "research-medium")
	})
	t.Run("writer row uses nearest lower independent row", func(t *testing.T) {
		t.Parallel()
		assertVerifierSeat(t, routing.ComplexityHigh, strings.ReplaceAll(roles, "high | claude", "high | codex"), "claude", "claude", "research-medium")
	})
	t.Run("legacy role table means low", func(t *testing.T) {
		t.Parallel()
		assertVerifierSeat(t, routing.ComplexityHigh, "\n| Role | Executor | Model |\n|---|---|---|\n| research | claude | research-low |\n", "claude", "claude", "research-low")
	})
}

func TestVerifierSkipsUnavailableResearchRow(t *testing.T) {
	t.Parallel()
	for _, mediumExecutor := range []string{"claude", "missing", "codex"} {
		t.Run(mediumExecutor, func(t *testing.T) {
			t.Parallel()
			roles := "\n| Role | Lane | Executor | Model |\n|---|---|---|---|\n" +
				"| research | low | claude | research-low |\n" +
				"| research | medium | " + mediumExecutor + " | research-medium |\n" +
				"| research | high | missing | research-high |\n"
			wantModel := "research-low"
			if mediumExecutor == "claude" {
				wantModel = "research-medium"
			}
			assertVerifierSeat(t, routing.ComplexityHigh, roles, "claude", "claude", wantModel)
		})
	}
}

func TestVerifierUsesIndependentBackendFallback(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		roles       string
		lowExecutor string
		wantName    string
		wantModel   string
	}{
		{"no research independent low", "", "claude", "claude", "fake-low"},
		{"no research same writer low", "", "codex", "codex", "fake-high"},
		{"no research unavailable low", "", "missing", "codex", "fake-high"},
		{"research same writer", "| research | low | codex | research-low |\n", "claude", "claude", "fake-low"},
		{"research unavailable", "| research | low | missing | research-low |\n", "claude", "claude", "fake-low"},
		{"research and low unavailable", "| research | low | missing | research-low |\n", "missing", "codex", "fake-high"},
		{"higher research ignored", "| research | low | codex | research-low |\n| research | critical | claude | research-critical |\n", "claude", "claude", "fake-low"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			roles := ""
			if tt.roles != "" {
				roles = "\n| Role | Lane | Executor | Model |\n|---|---|---|---|\n" + tt.roles
			}
			assertVerifierSeat(t, routing.ComplexityHigh, roles, tt.lowExecutor, tt.wantName, tt.wantModel)
		})
	}
}

func assertVerifierSeat(t *testing.T, lane routing.Complexity, roles, lowExecutor, wantName, wantModel string) {
	t.Helper()
	f := setup(t)
	payload, err := os.ReadFile(filepath.Join(f.skills, "adapters", "codex.md"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.skills, "adapters", "claude.md"), bytes.Replace(payload, []byte("name: codex"), []byte("name: claude"), 1), 0o644); err != nil {
		t.Fatal(err)
	}
	f.run(t, "add", "skills-batuta/adapters/claude.md")
	f.run(t, "commit", "-q", "-m", "test: add independent verifier adapter")
	var out bytes.Buffer
	r, err := New(context.Background(), f.options("default", &out))
	if err != nil {
		t.Fatal(err)
	}
	payload, err = os.ReadFile(filepath.Join(f.root, ".batuta", "routing.md"))
	if err != nil {
		t.Fatal(err)
	}
	table := strings.Replace(string(payload), "| low | * | codex |", "| low | * | "+lowExecutor+" |", 1) + roles
	r.table, err = routing.ParseRoutingTable([]byte(table))
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := r.adapterLocked("codex")
	if err != nil {
		t.Fatal(err)
	}
	r.backend = unavailableBackend{}
	ac := attemptContext{
		adapter: adapter, runtime: routing.RuntimeValue{Provider: "codex", Model: "fake-high"},
		worktree: attemptWorktree{Root: f.root}, base: f.base,
		plan: routing.PlanTask{TaskArtifact: routing.TaskArtifact{Title: "Add greeting one", Complexity: lane, Domain: routing.DomainBackend}},
	}
	verdict, err := r.verify(context.Background(), &ac, gates.ParseCriteria([]string{"greeting is correct"}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !verdict.Pass {
		t.Fatalf("verify() = %#v", verdict)
	}
	if ac.verifierDispatch.Executor != wantName || ac.verifierDispatch.Model != wantModel {
		t.Fatalf("verifier dispatch = %s/%s, want %s/%s", ac.verifierDispatch.Executor, ac.verifierDispatch.Model, wantName, wantModel)
	}
}

func TestCommitMessageKeepsCase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		title    string
		expected string
	}{
		{
			name:     "PRD acronym keeps case",
			title:    "PRD-v1.md in English",
			expected: "PRD-v1.md in English",
		},
		{
			name:     "gofmt lowercase acronym keeps case",
			title:    "gofmt the tree",
			expected: "gofmt the tree",
		},
		{
			name:     "Download deadline becomes download deadline",
			title:    "Download deadline",
			expected: "download deadline",
		},
		{
			name:     "single character uppercase",
			title:    "A",
			expected: "A",
		},
		{
			name:     "single character lowercase",
			title:    "a",
			expected: "a",
		},
		{
			name:     "empty title",
			title:    "",
			expected: "",
		},
		{
			name:     "two uppercase characters",
			title:    "UI improvement",
			expected: "UI improvement",
		},
		{
			name:     "non-letter second character",
			title:    "C-style formatting",
			expected: "C-style formatting",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			task := routing.PlanTask{
				TaskArtifact: routing.TaskArtifact{
					ID:    "task-1",
					Title: tt.title,
				},
			}
			got := commitMessage(task, "test-plan")
			lines := strings.Split(got, "\n")
			if len(lines) == 0 {
				t.Fatalf("commitMessage returned empty string")
			}
			parts := strings.SplitN(lines[0], ": ", 2)
			if len(parts) != 2 {
				t.Fatalf("expected subject line to contain ': ', got %q", lines[0])
			}
			subject := parts[1]
			if subject != tt.expected {
				t.Errorf("got subject %q, want %q", subject, tt.expected)
			}
		})
	}
}

func TestCommitMessageCutsAtWordBoundary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		title    string
		expected string
	}{
		{
			name:     "PRD-v1.md translation cut at word boundary before 68 bytes",
			title:    "PRD-v1.md in English: the historical PRD is translated in place, strings",
			expected: "PRD-v1.md in English: the historical PRD is translated in place",
		},
		{
			name:     "long title without spaces cut at 68",
			title:    strings.Repeat("a", 80),
			expected: strings.Repeat("a", 68),
		},
		{
			name:     "title <= 68 remains intact",
			title:    "Short title under limit",
			expected: "short title under limit",
		},
		{
			name:     "trims trailing punctuation after word boundary cut",
			title:    "This is a long title that needs to be cut here, and-more-words-exceeding-limit",
			expected: "this is a long title that needs to be cut here",
		},
		{
			name:     "trims trailing colon after cut",
			title:    "This is a long title that needs to be cut here: and-more-words-exceeding-limit",
			expected: "this is a long title that needs to be cut here",
		},
		{
			name:     "trims trailing semicolon after cut",
			title:    "This is a long title that needs to be cut here; and-more-words-exceeding-limit",
			expected: "this is a long title that needs to be cut here",
		},
		{
			name:     "trims trailing em-dash after cut",
			title:    "This is a long title that needs to be cut here— and-more-words-exceeding-limit",
			expected: "this is a long title that needs to be cut here",
		},
		{
			name:     "trims trailing hyphen after cut",
			title:    "This is a long title that needs to be cut here- and-more-words-exceeding-limit",
			expected: "this is a long title that needs to be cut here",
		},
		{
			name:     "space at byte 68 is the word boundary",
			title:    strings.Repeat("a", 62) + " word, and more words",
			expected: strings.Repeat("a", 62) + " word",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			task := routing.PlanTask{
				TaskArtifact: routing.TaskArtifact{
					ID:    "task-1",
					Title: tt.title,
				},
			}
			got := commitMessage(task, "test-plan")
			lines := strings.Split(got, "\n")
			if len(lines) == 0 {
				t.Fatalf("commitMessage returned empty string")
			}
			parts := strings.SplitN(lines[0], ": ", 2)
			if len(parts) != 2 {
				t.Fatalf("expected subject line to contain ': ', got %q", lines[0])
			}
			subject := parts[1]
			if subject != tt.expected {
				t.Errorf("got subject %q, want %q", subject, tt.expected)
			}
		})
	}
}

func TestCommitMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		task         routing.PlanTask
		slug         string
		expectedKind string
	}{
		{
			name: "docs domain",
			task: routing.PlanTask{
				TaskArtifact: routing.TaskArtifact{ID: "t-1", Domain: routing.DomainDocs, Title: "Update documentation"},
			},
			slug:         "my-plan",
			expectedKind: "docs",
		},
		{
			name: "testing domain",
			task: routing.PlanTask{
				TaskArtifact: routing.TaskArtifact{ID: "t-2", Domain: routing.DomainTesting, Title: "Add unit tests"},
			},
			slug:         "my-plan",
			expectedKind: "test",
		},
		{
			name: "fix title prefix",
			task: routing.PlanTask{
				TaskArtifact: routing.TaskArtifact{ID: "t-3", Title: "Fix memory leak"},
			},
			slug:         "my-plan",
			expectedKind: "fix",
		},
		{
			name: "repair title prefix",
			task: routing.PlanTask{
				TaskArtifact: routing.TaskArtifact{ID: "t-4", Title: "Repair broken socket"},
			},
			slug:         "my-plan",
			expectedKind: "fix",
		},
		{
			name: "bug in title",
			task: routing.PlanTask{
				TaskArtifact: routing.TaskArtifact{ID: "t-5", Title: "Resolve concurrency bug in worker"},
			},
			slug:         "my-plan",
			expectedKind: "fix",
		},
		{
			name: "refactor title prefix",
			task: routing.PlanTask{
				TaskArtifact: routing.TaskArtifact{ID: "t-6", Title: "Refactor router handlers"},
			},
			slug:         "my-plan",
			expectedKind: "refactor",
		},
		{
			name: "extract title prefix",
			task: routing.PlanTask{
				TaskArtifact: routing.TaskArtifact{ID: "t-7", Title: "Extract helper functions"},
			},
			slug:         "my-plan",
			expectedKind: "refactor",
		},
		{
			name: "rename title prefix",
			task: routing.PlanTask{
				TaskArtifact: routing.TaskArtifact{ID: "t-8", Title: "Rename ambiguous variable"},
			},
			slug:         "my-plan",
			expectedKind: "refactor",
		},
		{
			name: "infra domain",
			task: routing.PlanTask{
				TaskArtifact: routing.TaskArtifact{ID: "t-9", Domain: routing.DomainInfra, Title: "Setup docker build"},
			},
			slug:         "my-plan",
			expectedKind: "chore",
		},
		{
			name: "default feat",
			task: routing.PlanTask{
				TaskArtifact: routing.TaskArtifact{ID: "t-10", Title: "Build awesome feature"},
			},
			slug:         "my-plan",
			expectedKind: "feat",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := commitMessage(tt.task, tt.slug)
			expectedPrefix := tt.expectedKind + ": "
			if !strings.HasPrefix(got, expectedPrefix) {
				t.Errorf("expected commit message to start with %q, got %q", expectedPrefix, got)
			}
			expectedTrailer := "\n\nPlan " + tt.slug + ", " + tt.task.ID + ". Delivered by batuta loop.\n"
			if !strings.HasSuffix(got, expectedTrailer) {
				t.Errorf("expected commit message to end with trailer %q, got %q", expectedTrailer, got)
			}
		})
	}
}

// Each Open owns a distinct protocol connection, including verifier turns.
func loopACPTransport(t *testing.T, serve func(executor.Execution) (string, string)) *executor.TransportBackend {
	t.Helper()
	b := &executor.TransportBackend{
		Mode:   "acp",
		Lookup: func(string) (string, error) { return "/fixture/codex-acp", nil },
		VersionRunner: commandRunnerFunc(func(context.Context, publication.Command) (publication.CommandResult, error) {
			return publication.CommandResult{Stdout: []byte("fixture-v1")}, nil
		}),
	}
	for _, route := range []struct{ model, effort string }{{"fake-low", "low"}, {"fake-mid", "medium"}, {"fake-high", "high"}} {
		for _, effort := range []string{route.effort, ""} {
			b.Qualifications = append(b.Qualifications, executor.ACPQualification{Executor: "codex", Run: "codex-acp", Version: "fixture-v1", GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Model: route.model, Effort: effort, Permissions: true, Cleanup: true, AuthenticatedTask: true, Platform: true})
		}
	}
	b.ACP.PermissionPolicy = func(context.Context, executor.Execution, acp.PermissionRequest) string { return "" }
	b.ACP.Open = func(ctx context.Context, e executor.Execution) (*acp.Connection, func() error, error) {
		client, peer := net.Pipe()
		conn, err := acp.NewConnection(client, client, acp.Options{RequestTimeout: 5 * time.Second})
		if err != nil {
			client.Close()
			peer.Close()
			return nil, nil, err
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer peer.Close()
			reader := bufio.NewReader(peer)
			for _, method := range []string{"initialize", "session/new", "session/prompt"} {
				line, err := reader.ReadBytes('\n')
				if err != nil {
					t.Error(err)
					return
				}
				var request struct {
					ID     json.RawMessage
					Method string
				}
				if err := json.Unmarshal(line, &request); err != nil || request.Method != method {
					t.Errorf("ACP request: %s / %v", line, err)
					return
				}
				reply := `{"protocolVersion":1,"agentCapabilities":{}}`
				if method == "session/new" {
					reply = fmt.Sprintf(`{"sessionId":"task","configOptions":[{"id":"model","category":"model","type":"select","currentValue":%q,"options":[{"value":%q}]},{"id":"effort","category":"thought_level","type":"select","currentValue":%q,"options":[{"value":%q}]}]}`, e.Request.Model, e.Request.Model, e.Request.Effort, e.Request.Effort)
				}
				if method == "session/prompt" {
					output, stop := serve(e)
					fmt.Fprintln(peer, `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"task","update":{"sessionUpdate":"tool_call_update","status":"completed","title":"BATUTA-PROGRESS 9 DONE"}}}`)
					fmt.Fprintln(peer, `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"task","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"private-thought-canary"}}}}`)
					fmt.Fprintf(peer, "{\"jsonrpc\":\"2.0\",\"method\":\"session/update\",\"params\":{\"sessionId\":\"task\",\"update\":{\"sessionUpdate\":\"agent_message_chunk\",\"content\":{\"type\":\"text\",\"text\":%q}}}}\n", output)
					if stop == "quota_error" {
						fmt.Fprintf(peer, "{\"jsonrpc\":\"2.0\",\"id\":%s,\"error\":{\"code\":-32000,\"message\":\"quota exhausted\"}}\n", request.ID)
						continue
					}
					if stop == "disconnect" {
						return
					}
					reply = fmt.Sprintf(`{"stopReason":%q}`, stop)
				}
				fmt.Fprintf(peer, "{\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":%s}\n", request.ID, reply)
			}
			io.Copy(io.Discard, reader)
		}()
		return conn, func() error { peer.Close(); <-done; return nil }, nil
	}
	return b
}

func prepareACPAttempt(t *testing.T, f fixture, opts Options) *Runner {
	t.Helper()
	r, err := New(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := r.adapterLocked("codex")
	if err != nil {
		t.Fatal(err)
	}
	adapter.ACP = &executor.ACPLaunch{Run: "codex-acp", Version: "fixture-v1"}
	r.adapters["codex"] = adapter
	// Exercise the full attempt and candidate pipeline, before integration preflight.
	r.plan.Tasks[0].Complexity = routing.ComplexityHigh
	if err := r.open(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.graph.AdmitReadyWave(routing.ReadyWaveInput{IntegrationHeadSHA: f.base, RemainingSlots: 1, ReachableCommits: map[string]bool{f.base: true}}); err != nil {
		t.Fatal(err)
	}
	return r
}

func loopACPBeforeSubmission(t *testing.T, shutdown func() error) *executor.TransportBackend {
	t.Helper()
	b := loopACPTransport(t, func(executor.Execution) (string, string) {
		t.Error("unexpected prompt submission")
		return "", ""
	})
	b.ACP.Open = func(ctx context.Context, e executor.Execution) (*acp.Connection, func() error, error) {
		if err := os.WriteFile(filepath.Join(e.Request.Cwd, "shared.txt"), []byte("unverified startup work"), 0644); err != nil {
			return nil, nil, err
		}
		client, peer := net.Pipe()
		conn, err := acp.NewConnection(client, client, acp.Options{RequestTimeout: 5 * time.Second})
		if err != nil {
			client.Close()
			peer.Close()
			return nil, nil, err
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer peer.Close()
			reader := bufio.NewReader(peer)
			line, err := reader.ReadBytes('\n')
			if err != nil {
				t.Error(err)
				return
			}
			var request struct {
				ID     json.RawMessage
				Method string
			}
			if err := json.Unmarshal(line, &request); err != nil || request.Method != "initialize" {
				t.Errorf("ACP initialization: %s / %v", line, err)
				return
			}
			fmt.Fprintf(peer, "{\"jsonrpc\":\"2.0\",\"id\":%s,\"error\":{\"code\":-32000,\"message\":\"initialization rejected\"}}\n", request.ID)
			if extra, _ := io.ReadAll(reader); len(extra) != 0 {
				t.Errorf("requests after initialization rejection: %s", extra)
			}
		}()
		return conn, func() error { peer.Close(); <-done; return shutdown() }, nil
	}
	return b
}

func TestACPDispatchShutdownBeforeSubmission(t *testing.T) {
	for _, behavior := range []string{"shutdown", "canceled_shutdown", "verified_shutdown"} {
		t.Run(behavior, func(t *testing.T) {
			f := setup(t)
			var out bytes.Buffer
			opts := f.options("default", &out)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			opts.Transport = loopACPBeforeSubmission(t, func() error {
				calls++
				if behavior == "verified_shutdown" {
					return nil
				}
				if behavior == "canceled_shutdown" {
					cancel()
				}
				return errors.New("shutdown unresolved")
			})
			r := prepareACPAttempt(t, f, opts)
			defer r.Release()
			if _, err := r.runPreparingWaves(ctx); err != nil {
				t.Fatal(err)
			}
			task, _ := r.graph.Task("task_1")
			wantReconciliation := behavior != "verified_shutdown"
			if calls != 1 {
				t.Fatalf("dispatches = %d", calls)
			}
			var dispatch dispatchDetail
			records := readJournal(t, f, r.delivery)
			for _, record := range records {
				if record.Kind == KindDispatchResult {
					if err := json.Unmarshal(record.Detail, &dispatch); err != nil {
						t.Fatal(err)
					}
				}
			}
			var receipt executor.Receipt
			if err := json.Unmarshal(dispatch.Receipt, &receipt); err != nil {
				t.Fatal(err)
			}
			if dispatch.Backend != "acp" || dispatch.Submission != executor.SubmissionNotSubmitted || dispatch.ReconciliationRequired != wantReconciliation || (receipt.Transport.Failure == "shutdown") != wantReconciliation {
				t.Fatalf("incorrect dispatch evidence: %+v receipt=%+v", dispatch, receipt)
			}
			if !wantReconciliation {
				if task.State != routing.GraphTaskPreparing || len(task.Attempts) != 2 || task.Attempts[1].Runtime != task.Attempts[0].Runtime {
					t.Fatalf("safe non-submission lost ordinary retry: %+v", task)
				}
				return
			}
			if task.State != routing.GraphTaskBlocked || len(task.Attempts) != 1 || task.BlockerCode != blockerSubmissionUncertain {
				t.Fatalf("unverified shutdown retried: %+v", task)
			}
			if body, err := os.ReadFile(filepath.Join(task.Attempts[0].WorktreeRoot, "shared.txt")); err != nil || string(body) != "unverified startup work" {
				t.Fatalf("startup work lost: %q / %v", body, err)
			}
			counts := kinds(records)
			if counts[KindStarted] != 1 || counts[KindGates] != 0 || counts[KindCandidate] != 0 || counts[KindLimitWait] != 0 || counts[KindLimitFallback] != 0 {
				t.Fatalf("unverified shutdown consumed: %v", counts)
			}
		})
	}
}

func TestCLIDispatchFailureRetainsRetryPolicy(t *testing.T) {
	f := setup(t)
	var out bytes.Buffer
	r := prepareACPAttempt(t, f, f.options("default", &out))
	defer r.Release()
	r.backend = unavailableBackend{}
	if _, err := r.runPreparingWaves(context.Background()); err != nil {
		t.Fatal(err)
	}
	task, _ := r.graph.Task("task_1")
	if task.State != routing.GraphTaskPreparing || len(task.Attempts) != 2 || task.Attempts[1].Runtime != task.Attempts[0].Runtime {
		t.Fatalf("CLI failure lost ordinary retry: %+v", task)
	}
}

func TestACPAttemptPipelineAndIndependentVerifier(t *testing.T) {
	f := setup(t)
	var out bytes.Buffer
	opts := f.options("default", &out)
	tasks, verifications := 0, 0
	opts.Transport = loopACPTransport(t, func(e executor.Execution) (string, string) {
		tasks++
		if e.Request.Brief == "" || e.Request.Prompt != "" {
			t.Error("task did not receive its brief")
		}
		if err := os.MkdirAll(filepath.Join(e.Request.Cwd, "out"), 0755); err != nil {
			t.Error(err)
		}
		if err := os.WriteFile(filepath.Join(e.Request.Cwd, "out/1.txt"), []byte("ok\n"), 0644); err != nil {
			t.Error(err)
		}
		return "raw-stream-canary\nBATUTA-PROGRESS 1 START\nBATUTA-PROGRESS 1 DONE\n", "end_turn"
	})
	opts.VerifierTransport = loopACPTransport(t, func(e executor.Execution) (string, string) {
		verifications++
		if e.Request.Brief != "" || !strings.Contains(e.Request.Prompt, "independent read-only verifier") {
			t.Error("verifier reused task prompt")
		}
		return "TASK 1: DONE\n", "end_turn"
	})
	r := prepareACPAttempt(t, f, opts)
	if _, err := r.runPreparingWaves(context.Background()); err != nil {
		t.Fatal(err)
	}
	if tasks != 1 || verifications != 1 || len(r.candidates) != 1 {
		t.Fatalf("tasks=%d verifiers=%d candidates=%d\n%s", tasks, verifications, len(r.candidates), out.String())
	}
	records := readJournal(t, f, r.delivery)
	progress, reports := 0, 0
	for _, record := range records {
		if record.Kind == KindProgress {
			progress++
			if bytes.Contains(record.Detail, []byte(`"criterion":9`)) {
				t.Fatal("tool update fabricated DONE")
			}
		}
		if record.Kind == KindGates {
			var report gates.Report
			if err := json.Unmarshal(record.Detail, &report); err != nil {
				t.Fatal(err)
			}
			if !report.Passed || !report.Finished.Pass || !report.Tree.Pass || !report.Tests.Pass || !report.Scope.Pass || len(report.Proofs) != 1 || !report.Proofs[0].Pass || report.Verifier == nil || !report.Verifier.Pass {
				t.Fatalf("gates: %+v", report)
			}
			relativeLog, err := filepath.Rel(r.root, r.runLogPath("task_1", 1))
			if err != nil || report.Finished.Detail != "executor log: "+filepath.ToSlash(relativeLog) {
				t.Fatalf("missing owned log reference: %q / %v", report.Finished.Detail, err)
			}
			reports++
		}
		if bytes.Contains(record.Detail, []byte("raw-stream-canary")) || bytes.Contains(record.Detail, []byte("private-thought-canary")) {
			t.Fatal("raw output replaced compact journal evidence")
		}
	}
	if progress != 2 || reports != 1 || strings.Contains(out.String(), "raw-stream-canary") {
		t.Fatalf("progress=%d reports=%d output=%s", progress, reports, out.String())
	}
	log, err := os.ReadFile(r.runLogPath("task_1", 1))
	if err != nil || !bytes.Contains(log, []byte("raw-stream-canary")) || bytes.Contains(log, []byte("private-thought-canary")) {
		t.Fatalf("log=%s err=%v", log, err)
	}
}

func TestACPAttemptDoesNotReplayAfterSubmission(t *testing.T) {
	for _, stop := range []string{"disconnect", "max_tokens", "end_turn"} {
		t.Run(stop, func(t *testing.T) {
			f := setup(t)
			var out bytes.Buffer
			opts := f.options("default", &out)
			calls := 0
			opts.Transport = loopACPTransport(t, func(e executor.Execution) (string, string) {
				calls++
				if err := os.WriteFile(filepath.Join(e.Request.Cwd, "shared.txt"), []byte("partial work"), 0644); err != nil {
					t.Error(err)
				}
				return "Rate limit reached reset_at=4102444800\n", stop
			})
			r := prepareACPAttempt(t, f, opts)
			if _, err := r.runPreparingWaves(context.Background()); err != nil {
				t.Fatal(err)
			}
			task, _ := r.graph.Task("task_1")
			if calls != 1 || len(task.Attempts) != 1 || task.State != routing.GraphTaskBlocked {
				t.Fatalf("calls=%d task=%+v\n%s", calls, task, out.String())
			}
			root := task.Attempts[0].WorktreeRoot
			if body, err := os.ReadFile(filepath.Join(root, "shared.txt")); err != nil || string(body) != "partial work" {
				t.Fatalf("partial work lost: %q / %v", body, err)
			}
			counts := kinds(readJournal(t, f, r.delivery))
			if counts[KindLimitWait] != 0 || counts[KindLimitFallback] != 0 {
				t.Fatalf("replayed ACP: %v", counts)
			}
		})
	}
}

func TestACPAttemptHonorsIndependentGates(t *testing.T) {
	for _, gate := range []string{"cli-verifier", "tests", "scope", "proof", "verifier"} {
		t.Run(gate, func(t *testing.T) {
			f := setup(t)
			var out bytes.Buffer
			opts := f.options("default", &out)
			opts.Transport = loopACPTransport(t, func(e executor.Execution) (string, string) {
				if e.Request.Brief == "" {
					t.Error("task transport used for the independent CLI verifier")
				}
				path, body := "out/1.txt", "ok\n"
				switch gate {
				case "tests":
					body = "BROKEN\n"
				case "scope":
					if err := os.WriteFile(filepath.Join(e.Request.Cwd, "outside.txt"), []byte(body), 0644); err != nil {
						t.Error(err)
					}
				case "proof":
					path = "shared.txt"
				}
				if err := os.MkdirAll(filepath.Dir(filepath.Join(e.Request.Cwd, path)), 0755); err != nil {
					t.Error(err)
				}
				if err := os.WriteFile(filepath.Join(e.Request.Cwd, path), []byte(body), 0644); err != nil {
					t.Error(err)
				}
				return "BATUTA-PROGRESS 1 DONE\n", "end_turn"
			})
			if gate == "verifier" {
				opts.VerifierTransport = loopACPTransport(t, func(executor.Execution) (string, string) {
					return "TASK 1: INCOMPLETE — missing greeting behavior\n", "end_turn"
				})
			}
			r := prepareACPAttempt(t, f, opts)
			if _, err := r.runPreparingWaves(context.Background()); err != nil {
				t.Fatal(err)
			}
			var report gates.Report
			for _, record := range readJournal(t, f, r.delivery) {
				if record.Kind == KindGates {
					if err := json.Unmarshal(record.Detail, &report); err != nil {
						t.Fatal(err)
					}
				}
			}
			if report.TaskID != "task_1" || !report.Finished.Pass || !report.Tree.Pass || report.Verifier == nil {
				t.Fatalf("missing gate evidence: %+v", report)
			}
			wantPass := gate == "cli-verifier"
			if report.Passed != wantPass || (len(r.candidates) == 1) != wantPass {
				t.Fatalf("candidates=%d gates=%+v", len(r.candidates), report)
			}
			if !wantPass && !strings.Contains(strings.Join(report.Failures(), "\n"), "gate "+gate) {
				t.Fatalf("missing %s failure: %+v", gate, report)
			}
		})
	}
}

func TestACPVerifierRejectsIncompleteAndMutatingTurns(t *testing.T) {
	for _, behavior := range []string{"disconnect", "max_tokens", "mutation", "mutation-disconnect"} {
		t.Run(behavior, func(t *testing.T) {
			f := setup(t)
			var out bytes.Buffer
			opts := f.options("default", &out)
			opts.VerifierTransport = loopACPTransport(t, func(e executor.Execution) (string, string) {
				if strings.HasPrefix(behavior, "mutation") {
					if err := os.WriteFile(filepath.Join(e.Request.Cwd, "unauthorized.txt"), []byte("mutation"), 0644); err != nil {
						t.Error(err)
					}
				}
				stop := behavior
				if behavior == "mutation" {
					stop = "end_turn"
				} else if behavior == "mutation-disconnect" {
					stop = "disconnect"
				}
				return "TASK 1: DONE\n", stop
			})
			r := prepareACPAttempt(t, f, opts)
			ac := attemptContext{
				adapter: r.adapters["codex"], runtime: routing.RuntimeValue{Provider: "codex", Model: "fake-high"},
				worktree: attemptWorktree{Root: f.root}, base: f.base,
				plan: r.plan.Tasks[0],
			}
			verdict, err := r.verify(context.Background(), &ac, gates.ParseCriteria([]string{"greeting is correct"}), nil)
			if err != nil {
				t.Fatal(err)
			}
			if verdict.Pass || (strings.HasPrefix(behavior, "mutation") && !strings.Contains(verdict.Signal, "wrote to the tree")) {
				t.Fatalf("verifier accepted %s: %+v", behavior, verdict)
			}
		})
	}
}

func TestACPDispatchIntentPrecedesPrompt(t *testing.T) {
	f := setup(t)
	var out bytes.Buffer
	opts := f.options("default", &out)
	var r *Runner
	opts.Transport = loopACPTransport(t, func(e executor.Execution) (string, string) {
		records := readJournal(t, f, r.delivery)
		var intent struct {
			Backend     string `json:"backend"`
			Executor    string `json:"executor"`
			Model       string `json:"model"`
			Reasoning   string `json:"reasoning"`
			Workspace   string `json:"workspace"`
			BriefDigest string `json:"brief_digest"`
			RunID       string `json:"run_id"`
			Execution   int    `json:"execution"`
		}
		found := false
		for _, record := range records {
			if record.Kind != "dispatch_intent" {
				continue
			}
			if err := json.Unmarshal(record.Detail, &intent); err != nil {
				t.Error(err)
			}
			found = true
		}
		wantDigest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(e.Request.Brief)))
		if !found || intent.Backend != "acp" || intent.Executor != "codex" || intent.Model != e.Request.Model || intent.Reasoning != e.Request.Effort || intent.Workspace != e.Request.Cwd || intent.BriefDigest != wantDigest || intent.RunID == "" || intent.Execution != 1 {
			t.Errorf("prompt lacks durable dispatch identity: %+v", intent)
		}
		return "", "disconnect"
	})
	r = prepareACPAttempt(t, f, opts)
	if _, err := r.runPreparingWaves(context.Background()); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, record := range readJournal(t, f, r.delivery) {
		if record.Kind != "dispatch_result" {
			continue
		}
		var detail struct {
			Receipt *executor.Receipt `json:"receipt"`
		}
		if err := json.Unmarshal(record.Detail, &detail); err != nil {
			t.Fatal(err)
		}
		if detail.Receipt == nil || detail.Receipt.Submission.State != executor.SubmissionUncertain {
			t.Fatalf("missing uncertain receipt: %s", record.Detail)
		}
		found = true
	}
	if !found {
		t.Fatal("no durable dispatch result")
	}
}

func TestACPUncertainWorkSurvivesFinalization(t *testing.T) {
	for _, stop := range []string{"disconnect", "quota_error", "unknown", "end_turn"} {
		t.Run(stop, func(t *testing.T) {
			f := setup(t)
			var out bytes.Buffer
			opts := f.options("default", &out)
			calls := 0
			opts.Transport = loopACPTransport(t, func(e executor.Execution) (string, string) {
				calls++
				if err := os.WriteFile(filepath.Join(e.Request.Cwd, "shared.txt"), []byte("unverified work"), 0644); err != nil {
					t.Error(err)
				}
				return "Rate limit reached reset_at=4102444800\n", stop
			})
			r := prepareACPAttempt(t, f, opts)
			state, err := r.Run(context.Background())
			if err != nil || state != StateBlocked {
				t.Fatalf("Run = %s, %v\n%s", state, err, out.String())
			}
			task, _ := r.graph.Task("task_1")
			if calls != 1 || len(task.Attempts) != 1 || task.BlockerCode != blockerSubmissionUncertain {
				t.Fatalf("uncertain attempt replayed: calls=%d task=%+v", calls, task)
			}
			if body, err := os.ReadFile(filepath.Join(task.Attempts[0].WorktreeRoot, "shared.txt")); err != nil || string(body) != "unverified work" {
				t.Fatalf("unverified workspace lost: %q / %v", body, err)
			}
			records := readJournal(t, f, r.delivery)
			counts := kinds(records)
			if counts[KindLimitWait] != 0 || counts[KindLimitFallback] != 0 || counts[KindCandidate] != 0 || counts[KindGates] != 0 {
				t.Fatalf("uncertain work consumed: %v", counts)
			}
			parked, err := r.git.Parked(context.Background(), r.plan.Slug)
			if err != nil || len(parked) == 0 {
				t.Fatalf("parked evidence lost: %v / %v", parked, err)
			}
		})
	}
}

func TestCLITaskPreservesUncertainACPVerifier(t *testing.T) {
	for _, behavior := range []string{"disconnect", "shutdown"} {
		t.Run(behavior, func(t *testing.T) {
			f := setup(t)
			var out bytes.Buffer
			opts := f.options("default", &out)
			calls := 0
			var r *Runner
			opts.VerifierTransport = loopACPTransport(t, func(e executor.Execution) (string, string) {
				calls++
				var task, verifier dispatchDetail
				for _, record := range readJournal(t, f, r.delivery) {
					switch record.Kind {
					case KindDispatchResult:
						if err := json.Unmarshal(record.Detail, &task); err != nil {
							t.Error(err)
						}
					case "verifier_dispatch_intent":
						if err := json.Unmarshal(record.Detail, &verifier); err != nil {
							t.Error(err)
						}
					}
				}
				if task.Backend != "cli" || verifier.Backend != "acp" || verifier.RunID == "" || verifier.RunID == task.RunID || verifier.Model != e.Request.Model || verifier.Executor != e.Adapter.Name || verifier.Reasoning != e.Request.Effort || verifier.Workspace != e.Request.Cwd || verifier.BriefDigest != digestString(e.Request.Prompt) || verifier.Execution != 1 {
					t.Errorf("independent intent missing: task=%+v verifier=%+v", task, verifier)
				}
				if behavior == "disconnect" {
					return "TASK 1: DONE\n", "disconnect"
				}
				return "TASK 1: DONE\n", "end_turn"
			})
			if behavior == "shutdown" {
				open := opts.VerifierTransport.ACP.Open
				opts.VerifierTransport.ACP.Open = func(ctx context.Context, e executor.Execution) (*acp.Connection, func() error, error) {
					conn, shutdown, err := open(ctx, e)
					return conn, func() error { return errors.Join(shutdown(), errors.New("shutdown unresolved")) }, err
				}
			}
			r = prepareACPAttempt(t, f, opts)
			state, err := r.Run(context.Background())
			if err != nil || state != StateBlocked {
				t.Fatalf("Run = %s, %v\n%s", state, err, out.String())
			}
			task, _ := r.graph.Task("task_1")
			if calls != 1 || len(task.Attempts) != 1 || task.BlockerCode != blockerSubmissionUncertain {
				t.Fatalf("uncertain verifier replayed: calls=%d task=%+v", calls, task)
			}
			if body, err := os.ReadFile(filepath.Join(task.Attempts[0].WorktreeRoot, "out", "1.txt")); err != nil || string(body) != "ok\n" {
				t.Fatalf("task work lost: %q / %v", body, err)
			}
			var receipt *executor.Receipt
			records := readJournal(t, f, r.delivery)
			for _, record := range records {
				if record.Kind == "verifier_dispatch_result" {
					var detail struct {
						Receipt *executor.Receipt `json:"receipt"`
					}
					if err := json.Unmarshal(record.Detail, &detail); err != nil {
						t.Fatal(err)
					}
					receipt = detail.Receipt
				}
			}
			if receipt == nil || receipt.Submission.State == executor.SubmissionNotSubmitted || receipt.Transport.Outcome == executor.TransportCompleted {
				t.Fatalf("uncertain receipt lost: %+v", receipt)
			}
			counts := kinds(records)
			if counts[KindStarted] != 1 || counts[KindCandidate] != 0 || counts[KindLimitWait] != 0 || counts[KindLimitFallback] != 0 {
				t.Fatalf("uncertain work consumed: %v", counts)
			}
		})
	}
}
