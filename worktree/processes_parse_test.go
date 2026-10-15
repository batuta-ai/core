package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type tableScanner struct {
	processes []Process
	err       error
	calls     int
}

func (s *tableScanner) Inside(context.Context, string) ([]Process, error) {
	s.calls++
	return s.processes, s.err
}

func testInsideEnv(scanner ProcessScanner, home string) insideEnv {
	return insideEnv{
		scanner:   scanner,
		home:      home,
		callerPID: 10,
		parentPID: func(pid int) (int, error) {
			if pid == 10 {
				return 1, nil
			}
			return 0, fmt.Errorf("unexpected PID %d", pid)
		},
	}
}

func TestInsideRejectsUnsafeTarget(t *testing.T) {
	t.Parallel()
	base := tempDir(t)
	home := filepath.Join(base, "home", "user")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "linked-home")
	if err := os.Symlink(home, link); err != nil {
		t.Fatal(err)
	}
	root := string(filepath.Separator)
	if volume := filepath.VolumeName(base); volume != "" {
		root = volume + root
	}
	cases := []struct {
		name string
		dir  string
	}{
		{"empty", ""},
		{"relative", "relative/path/tree"},
		{"unclean", filepath.Join(base, "tree") + string(filepath.Separator) + ".." + string(filepath.Separator) + "tree"},
		{"root", root},
		{"home", home},
		{"home ancestor", base},
		{"too shallow", filepath.Join(root, "one", "two")},
		{"symlink to home", link},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			scanner := &tableScanner{}
			got, err := insideWith(context.Background(), testInsideEnv(scanner, home), tc.dir)
			if !errors.Is(err, ErrUnsafeProcessTarget) || len(got) != 0 || scanner.calls != 0 {
				t.Fatalf("insideWith(%q) = %v, %v; scanner calls = %d", tc.dir, got, err, scanner.calls)
			}
		})
	}
}

func TestInsideMissingDir(t *testing.T) {
	t.Parallel()
	base := tempDir(t)
	dir := filepath.Join(base, "missing", "worktree")
	scanner := &tableScanner{}
	got, err := insideWith(context.Background(), testInsideEnv(scanner, filepath.Join(base, "home")), dir)
	if err != nil || len(got) != 0 || scanner.calls != 0 {
		t.Fatalf("insideWith(missing) = %v, %v; scanner calls = %d", got, err, scanner.calls)
	}
}

func TestInsideBoundary(t *testing.T) {
	t.Parallel()
	base := tempDir(t)
	dir := filepath.Join(base, "wt")
	if err := os.MkdirAll(filepath.Join(dir, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "linked-wt")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	want := []Process{{PID: 2, Cwd: dir}, {PID: 3, Cwd: filepath.Join(dir, "child")}}
	for _, target := range []string{dir, link} {
		t.Run(filepath.Base(target), func(t *testing.T) {
			t.Parallel()
			scanner := &tableScanner{processes: []Process{
				{PID: 2, Cwd: dir},
				{PID: 3, Cwd: filepath.Join(dir, "child")},
				{PID: 4, Cwd: dir + "2"},
			}}
			got, err := insideWith(context.Background(), testInsideEnv(scanner, filepath.Join(base, "home")), target)
			if err != nil || !reflect.DeepEqual(got, want) || scanner.calls != 1 {
				t.Fatalf("insideWith(%q) = %v, %v; scanner calls = %d", target, got, err, scanner.calls)
			}
		})
	}
}

func TestInsideExcludesAncestors(t *testing.T) {
	t.Parallel()
	base := tempDir(t)
	dir := filepath.Join(base, "wt")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		parents map[int]int
		procs   []Process
		want    []Process
	}{
		{
			name:    "parent omitted from scan",
			parents: map[int]int{10: 20, 20: 30, 30: 1},
			procs:   []Process{{PID: 10, Cwd: dir}, {PID: 30, Cwd: dir}, {PID: 50, Cwd: dir}},
			want:    []Process{{PID: 50, Cwd: dir}},
		},
		{
			name:    "cycle",
			parents: map[int]int{10: 20, 20: 10},
			procs:   []Process{{PID: 10, Cwd: dir}, {PID: 20, Cwd: dir}, {PID: 50, Cwd: dir}},
			want:    []Process{{PID: 50, Cwd: dir}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			scanner := &tableScanner{processes: tc.procs}
			env := testInsideEnv(scanner, filepath.Join(base, "home"))
			env.parentPID = func(pid int) (int, error) { return tc.parents[pid], nil }
			got, err := insideWith(context.Background(), env, dir)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("insideWith() = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}

func TestInsideAncestryFailsClosed(t *testing.T) {
	t.Parallel()
	base := tempDir(t)
	dir := filepath.Join(base, "wt")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	scanner := &tableScanner{processes: []Process{{PID: 50, Cwd: dir}}}
	env := testInsideEnv(scanner, filepath.Join(base, "home"))
	env.parentPID = func(pid int) (int, error) {
		if pid == 10 {
			return 20, nil
		}
		return 0, errors.New("lookup failed")
	}
	got, err := insideWith(context.Background(), env, dir)
	if err == nil || len(got) != 0 {
		t.Fatalf("insideWith() = %v, %v; want error and no processes", got, err)
	}
}

func TestParseProcessTables(t *testing.T) {
	t.Parallel()
	t.Run("lsof", func(t *testing.T) {
		t.Parallel()
		input := "p12\ncnext dev\nn/tmp/tree one\np13\ncshort\nn/tmp/tree two\n"
		got, err := parseLsof([]byte(input))
		want := []Process{{PID: 12, Command: "next dev", Cwd: "/tmp/tree one"}, {PID: 13, Command: "short", Cwd: "/tmp/tree two"}}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("parseLsof() = %v, %v; want %v", got, err, want)
		}
	})
	t.Run("partial lsof", func(t *testing.T) {
		t.Parallel()
		got, err := parseLsof([]byte("p12\ncfirst\nn/tmp/one\npinvalid\np13\ncsecond\n"))
		var partial *PartialScanError
		if !errors.As(err, &partial) || len(got) != 1 || got[0].PID != 12 {
			t.Fatalf("parseLsof() = %v, %v; want first record and partial error", got, err)
		}
	})
	for _, tc := range []struct {
		name    string
		command string
		state   byte
		ppid    int
		ticks   int64
	}{
		{"spaces", "next dev", 'S', 17, 12345},
		{"close parenthesis", "worker) with ) space", 'R', 18, 23456},
		{"truncated", "very-shortened", 'Z', 19, 34567},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fields := make([]string, 20)
			fields[0] = string(tc.state)
			fields[1] = fmt.Sprint(tc.ppid)
			for i := 2; i < len(fields); i++ {
				fields[i] = "0"
			}
			fields[19] = fmt.Sprint(tc.ticks)
			proc, state, err := parseProcStat("42 (" + tc.command + ") " + strings.Join(fields, " "))
			if err != nil || proc.PID != 42 || proc.PPID != tc.ppid || proc.Command != tc.command || proc.Start != time.Unix(0, tc.ticks) || state != tc.state {
				t.Fatalf("parseProcStat() = %+v, %q, %v", proc, state, err)
			}
		})
	}
}

func TestInsidePartialScan(t *testing.T) {
	t.Parallel()
	base := tempDir(t)
	dir := filepath.Join(base, "wt")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	partial := &PartialScanError{Warning: "one process was unreadable"}
	scanner := &tableScanner{processes: []Process{{PID: 2, Cwd: dir}, {PID: 3, Cwd: dir + "2"}}, err: partial}
	got, err := insideWith(context.Background(), testInsideEnv(scanner, filepath.Join(base, "home")), dir)
	if !errors.Is(err, partial) || !reflect.DeepEqual(got, []Process{{PID: 2, Cwd: dir}}) {
		t.Fatalf("insideWith() = %v, %v", got, err)
	}
}

func TestUnsupportedScanner(t *testing.T) {
	t.Parallel()
	got, err := (unsupportedScanner{}).Inside(context.Background(), "/some/directory")
	if !errors.Is(err, ErrProcessScanUnsupported) || len(got) != 0 {
		t.Fatalf("Inside() = %v, %v", got, err)
	}
}
