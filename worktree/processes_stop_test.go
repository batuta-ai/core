package worktree

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type unsupportedRecordingSignaller struct{ calls int }

func (s *unsupportedRecordingSignaller) Open(int) (processSignal, error) {
	s.calls++
	return nil, nil
}

func TestStopProcessesUnsupported(t *testing.T) {
	t.Parallel()
	signaller := &unsupportedRecordingSignaller{}
	stopper := unsupportedStopper{signaller: signaller}
	got, err := stopper.Stop(context.Background(), "/tmp/example/worktree", []Process{{PID: 42}}, 0)
	if !errors.Is(err, ErrProcessScanUnsupported) || len(got) != 0 || signaller.calls != 0 {
		t.Fatalf("unsupported Stop() = %v, %v; signaller calls = %d", got, err, signaller.calls)
	}
}

func TestProcessesNoBareSyscall(t *testing.T) {
	t.Parallel()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.Contains(name, "_linux.go") || strings.Contains(name, "_darwin.go") || strings.Contains(name, "_windows.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(string(data), "//go:build ") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, data, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if path == "syscall" || strings.HasPrefix(path, "golang.org/x/sys") {
				t.Errorf("untagged file %s imports %s", name, path)
			}
		}
	}
}
