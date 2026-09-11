package loop

import (
	"bufio"
	"bytes"
	"context"
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
	verdict := r.verify(context.Background(), ac, gates.ParseCriteria([]string{"greeting is correct"}), nil)
	if !verdict.Pass || !strings.HasPrefix(verdict.Signal, "codex/fake-low: ") {
		t.Fatalf("verify() = %#v", verdict)
	}
	evidence, err := os.ReadFile(filepath.Join(f.state, "verifier-git-config"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(evidence), "command line:\tfalse") {
		t.Fatalf("verifier lost CLI environment: %s", evidence)
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
			verdict := r.verify(context.Background(), ac, gates.ParseCriteria([]string{"greeting is correct"}), nil)
			if verdict.Pass || (strings.HasPrefix(behavior, "mutation") && !strings.Contains(verdict.Signal, "wrote to the tree")) {
				t.Fatalf("verifier accepted %s: %+v", behavior, verdict)
			}
		})
	}
}
