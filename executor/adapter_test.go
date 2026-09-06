package executor

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

const codexAdapter = `---
name: codex
executable: codex
run: codex exec --sandbox workspace-write {cwd_flag} {model_flags} "{brief}" < /dev/null
run_file: codex exec --sandbox workspace-write {cwd_flag} {model_flags} "Follow the instructions in {brief_file}" < /dev/null
model_flags: -m {model} -c model_reasoning_effort="{effort}"
readonly: codex exec --sandbox read-only {cwd_flag} -m {model} "{prompt}" < /dev/null
available: command -v codex && codex login status
models: codex debug models --bundled
finished: exit_code
limit_regex: "rate limit reached|quota exceeded|usage limit reached|too many requests"
brief_limit_lines: 3
cwd_flag: --cd {cwd}
---

# Adapter: codex
`

func TestParseAdapterReadsTheFrontmatterContract(t *testing.T) {
	adapter, err := ParseAdapter([]byte(codexAdapter))
	if err != nil {
		t.Fatalf("ParseAdapter() error = %v", err)
	}
	if adapter.Name != "codex" || adapter.Executable != "codex" || adapter.BriefLimitLines != 3 || adapter.CwdFlag != "--cd {cwd}" ||
		adapter.LimitRegex != "rate limit reached|quota exceeded|usage limit reached|too many requests" || adapter.Finished != "exit_code" {
		t.Fatalf("adapter = %#v", adapter)
	}
	quoted := strings.Replace(codexAdapter, "readonly: codex exec", "readonly: 'codex exec", 1)
	quoted = strings.Replace(quoted, `"{prompt}" < /dev/null`+"\n", `"{prompt}" < /dev/null'`+"\n", 1)
	if got, err := ParseAdapter([]byte(quoted)); err != nil || !strings.HasSuffix(got.Readonly, "< /dev/null") {
		t.Fatalf("quoted scalar = %q, %v", got.Readonly, err)
	}
	for name, payload := range map[string]string{
		"no block":     "name: codex\n",
		"missing key":  "---\nname: codex\nrun: x\n---\n",
		"bad line":     strings.Replace(codexAdapter, "finished: exit_code", "finished exit_code", 1),
		"bad regex":    strings.Replace(codexAdapter, `limit_regex: "rate`, `limit_regex: "(rate`, 1),
		"bad limit":    strings.Replace(codexAdapter, "brief_limit_lines: 3", "brief_limit_lines: many", 1),
		"unterminated": strings.Replace(codexAdapter, `models: codex`, `models: "codex`, 1),
	} {
		if _, err := ParseAdapter([]byte(payload)); err == nil {
			t.Fatalf("ParseAdapter(%s) should fail", name)
		}
	}
}

func TestTokenizeSplitsLikeAShellWithoutRunningOne(t *testing.T) {
	cases := map[string][]string{
		`codex exec --sandbox workspace-write "{brief}" < /dev/null`:         {"codex", "exec", "--sandbox", "workspace-write", "{brief}"},
		`env -u CLAUDECODE claude -p --model {model} "{prompt}" < /dev/null`: {"env", "-u", "CLAUDECODE", "claude", "-p", "--model", "{model}", "{prompt}"},
		`-m {model} -c model_reasoning_effort="{effort}"`:                    {"-m", "{model}", "-c", "model_reasoning_effort={effort}"},
		`agy -p "Read-only task: do not edit. {prompt}" --mode=plan`:         {"agy", "-p", "Read-only task: do not edit. {prompt}", "--mode=plan"},
		`opencode run 'it''s' "a \"quote\"" back\ slash`:                     {"opencode", "run", "its", `a "quote"`, "back slash"},
	}
	for line, want := range cases {
		got, err := Tokenize(line)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("Tokenize(%q) = %q, %v; want %q", line, got, err, want)
		}
	}
	for _, line := range []string{`a | b`, `a && b`, `a > out`, `a $(b)`, `a "$HOME"`, `a < other`, `a 'unterminated`, ``} {
		if _, err := Tokenize(line); err == nil {
			t.Fatalf("Tokenize(%q) should fail", line)
		}
	}
}

func TestCommandSubstitutesPlaceholdersPerToken(t *testing.T) {
	adapter, _ := ParseAdapter([]byte(codexAdapter))
	cwd := filepath.Join(string(filepath.Separator), "work", "tree")
	short := Request{Brief: "Goal: rename x to y", Cwd: cwd, Model: "gpt-5.6-terra", Effort: "high"}
	invocation, err := adapter.Command(short)
	if err != nil {
		t.Fatalf("Command() error = %v", err)
	}
	want := []string{"exec", "--sandbox", "workspace-write", "--cd", cwd, "-m", "gpt-5.6-terra", "-c", "model_reasoning_effort=high", "Goal: rename x to y"}
	if invocation.Executable != "codex" || !reflect.DeepEqual(invocation.Args, want) || invocation.Dir != cwd || invocation.UsedFile {
		t.Fatalf("invocation = %#v", invocation)
	}

	// The default model omits the model flags entirely.
	invocation, _ = adapter.Command(Request{Brief: "x", Cwd: cwd, Model: DefaultModel, Effort: "medium"})
	if strings.Join(invocation.Args, " ") != "exec --sandbox workspace-write --cd "+cwd+" x" {
		t.Fatalf("default model args = %q", invocation.Args)
	}

	// A brief over the line budget goes through run_file.
	long := Request{Brief: "a\nb\nc\nd", BriefFile: filepath.Join(cwd, ".batuta", "brief.md"), Cwd: cwd, Model: "m", Effort: "low"}
	invocation, err = adapter.Command(long)
	if err != nil || !invocation.UsedFile || invocation.Args[len(invocation.Args)-1] != "Follow the instructions in "+long.BriefFile {
		t.Fatalf("run_file invocation = %#v, %v", invocation, err)
	}
	if _, err := adapter.Command(Request{Brief: "a\nb\nc\nd", Cwd: cwd, Model: "m"}); err == nil {
		t.Fatal("long brief without a brief file should fail")
	}

	// cwd_flag: env means no flag; the runner sets the directory.
	env := strings.Replace(codexAdapter, "cwd_flag: --cd {cwd}", "cwd_flag: env", 1)
	adapter, _ = ParseAdapter([]byte(env))
	invocation, _ = adapter.Command(short)
	if strings.Contains(strings.Join(invocation.Args, " "), "--cd") || invocation.Dir != cwd {
		t.Fatalf("env cwd invocation = %#v", invocation)
	}

	readonly, err := adapter.ReadonlyCommand(Request{Prompt: "TASK 1?", Cwd: cwd, Model: "cheap"})
	if err != nil || strings.Join(readonly.Args, " ") != "exec --sandbox read-only -m cheap TASK 1?" {
		t.Fatalf("readonly = %#v, %v", readonly, err)
	}

	self, _ := ParseAdapter([]byte("---\nname: self\nexecutable: none\nrun: self\nreadonly: self\navailable: \"true\"\nmodels: declared\nfinished: self\n---\n"))
	if !self.IsSelf() {
		t.Fatal("self adapter not recognised")
	}
	if _, err := self.Command(short); err == nil {
		t.Fatal("self.Command should fail")
	}
}

func TestSubprocessExecutesAndAppliesTheOutcomeRules(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell script")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-codex")
	os.WriteFile(script, []byte("#!/bin/sh\necho \"args: $*\"\ncase \"$FAKE_MODE\" in\n  limit) echo 'Rate limit reached for gpt' >&2; exit 2;;\n  ask) echo 'thinking'; echo 'BATUTA-QUESTION: which table?'; exit 0;;\n  slow) sleep 5;;\n  *) exit 0;;\nesac\n"), 0o755)
	adapter, _ := ParseAdapter([]byte(codexAdapter))
	invocation := Invocation{Executable: script, Args: []string{"exec", "--cd", dir, "brief"}, Dir: dir}
	ctx := context.Background()

	run := func(mode string, timeout time.Duration) Result {
		t.Helper()
		result, err := Subprocess{Runner: NewSubprocess().Runner, Environment: []string{"FAKE_MODE=" + mode}}.Execute(ctx, adapter, invocation, timeout)
		if err != nil {
			t.Fatalf("Execute(%s) error = %v", mode, err)
		}
		return result
	}
	if result := run("ok", 0); !result.Finished || result.ExitCode != 0 || !strings.Contains(string(result.Stdout), "args: exec --cd") || len(result.Progress) != 0 {
		t.Fatalf("ok = %#v", result)
	}
	if result := run("limit", 0); result.Finished || !result.RateLimited || result.ExitCode != 2 {
		t.Fatalf("limit = %#v", result)
	}
	if result := run("ask", 0); !result.Finished || result.Question != "which table?" {
		t.Fatalf("ask = %#v", result)
	}
	if result := run("slow", 300*time.Millisecond); result.Finished || !result.TimedOut {
		t.Fatalf("slow = %#v", result)
	}
	if _, err := NewSubprocess().Execute(ctx, adapter, Invocation{Executable: "definitely-not-a-command-xyz", Dir: dir}, 0); err == nil {
		t.Fatal("missing executable should error")
	}
	if got := Tail([]byte("a\nb\nc\nd\n"), 2); got != "c\nd" {
		t.Fatalf("Tail() = %q", got)
	}
}

func TestLoadAdapterReadsFromTheSkillsRoot(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "adapters"), 0o755)
	os.WriteFile(filepath.Join(root, "adapters", "codex.md"), []byte(codexAdapter), 0o644)
	if adapter, err := LoadAdapter(root, "codex"); err != nil || adapter.Name != "codex" {
		t.Fatalf("LoadAdapter() = %#v, %v", adapter, err)
	}
	if _, err := LoadAdapter(root, "opencode"); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("LoadAdapter(missing) error = %v", err)
	}
	os.WriteFile(filepath.Join(root, "adapters", "agy.md"), []byte(codexAdapter), 0o644)
	if _, err := LoadAdapter(root, "agy"); err == nil {
		t.Fatal("LoadAdapter with a mismatched name should fail")
	}
	if _, err := LoadAdapter(root, "../etc"); err == nil {
		t.Fatal("LoadAdapter with a path should fail")
	}
}
