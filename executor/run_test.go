package executor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSubprocessAppendsGitConfigOverride(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	directory := t.TempDir()
	executable := filepath.Join(directory, "show-git-config")
	script := `#!/bin/sh
set -e
printf 'count=%s\nkey0=%s\nvalue0=%s\nkey1=%s\nvalue1=%s\n' \
  "$GIT_CONFIG_COUNT" "$GIT_CONFIG_KEY_0" "$GIT_CONFIG_VALUE_0" \
  "$GIT_CONFIG_KEY_1" "$GIT_CONFIG_VALUE_1"
git config --show-origin --get user.name
git config --show-origin --get commit.gpgsign
`
	if err := os.WriteFile(executable, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	subprocess := NewSubprocess()
	subprocess.Environment = []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=user.name",
		"GIT_CONFIG_VALUE_0=loop",
	}
	result, err := subprocess.Execute(context.Background(), Adapter{}, Invocation{Executable: executable, Dir: directory}, 0)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	output := string(result.Stdout)
	for _, want := range []string{
		"count=2",
		"key0=user.name",
		"value0=loop",
		"key1=commit.gpgsign",
		"value1=false",
		"command line:\tloop",
		"command line:\tfalse",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("stdout missing %q:\n%s", want, output)
		}
	}
}
