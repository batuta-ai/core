package executor

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/batuta-ai/core/executor/acp"
)

func TestWorktreePolicyAllowsInsideLocations(t *testing.T) {
	t.Parallel()
	cwd := resolvedCwd(t)
	existing := filepath.Join(cwd, "exists.txt")
	if err := os.WriteFile(existing, []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	realDir := filepath.Join(cwd, "real")
	if err := os.Mkdir(realDir, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(cwd, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		paths []string
	}{
		{"existing file", []string{existing}},
		{"missing file", []string{filepath.Join(cwd, "new.txt")}},
		{"missing nested", []string{filepath.Join(cwd, "missing", "dir", "file.go")}},
		{"cwd", []string{cwd}},
		{"symlink stays inside", []string{filepath.Join(link, "created.txt")}},
		{"every location inside", []string{existing, filepath.Join(cwd, "other.txt")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			request := acp.PermissionRequest{
				Options: []acp.PermissionOption{
					{OptionID: "always", Kind: "allow_always"},
					{OptionID: "yes", Kind: "allow_once"},
				},
			}
			request.ToolCall.Locations = permissionLocations(tc.paths...)
			got := WorktreePermissionPolicy(context.Background(), Execution{Request: Request{Cwd: cwd}}, request)
			if got != "yes" {
				t.Fatalf("got %q, want allow_once ID", got)
			}
		})
	}
}

func TestWorktreePolicyRejects(t *testing.T) {
	t.Parallel()
	cwd := resolvedCwd(t)
	outside := resolvedCwd(t)
	inside := filepath.Join(cwd, "inside.txt")
	escape := filepath.Join(cwd, "escape")
	if err := os.Symlink(outside, escape); err != nil {
		t.Fatal(err)
	}
	root := resolvedCwd(t)
	work := filepath.Join(root, "work")
	evil := filepath.Join(root, "work-evil")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(evil, 0700); err != nil {
		t.Fatal(err)
	}
	danglingOutside := filepath.Join(cwd, "dangling-out")
	if err := os.Symlink(filepath.Join(outside, "missing-target"), danglingOutside); err != nil {
		t.Fatal(err)
	}
	danglingInside := filepath.Join(cwd, "dangling-in")
	if err := os.Symlink(filepath.Join(cwd, "missing-target"), danglingInside); err != nil {
		t.Fatal(err)
	}
	dotdot := cwd + string(filepath.Separator) + ".." + string(filepath.Separator) + filepath.Base(outside) + string(filepath.Separator) + "secret"
	dotDotComponent := cwd + string(filepath.Separator) + "missing" + string(filepath.Separator) + ".." + string(filepath.Separator) + "file.txt"
	request := func(kind string, paths ...string) acp.PermissionRequest {
		var req acp.PermissionRequest
		if kind != "" {
			req.Options = []acp.PermissionOption{{OptionID: "yes", Kind: kind}}
		}
		req.ToolCall.Locations = permissionLocations(paths...)
		return req
	}
	execute := request("allow_once")
	execute.ToolCall.Kind = "execute"
	onlyAlways := request("allow_always", inside)
	onlyAlways.Options = []acp.PermissionOption{
		{OptionID: "always", Kind: "allow_always"},
		{OptionID: "no", Kind: "reject_once"},
	}
	for _, tc := range []struct {
		name    string
		cwd     string
		request acp.PermissionRequest
	}{
		{"no locations", cwd, request("allow_once")},
		{"execute", cwd, execute},
		{"relative location", cwd, request("allow_once", "relative.txt")},
		{"relative dotted location", cwd, request("allow_once", "./file.txt")},
		{"empty path", cwd, request("allow_once", "")},
		{"outside location", cwd, request("allow_once", filepath.Join(outside, "file"))},
		{"symlink escape", cwd, request("allow_once", filepath.Join(escape, "file"))},
		{"dangling_symlink_outside", cwd, request("allow_once", filepath.Join(danglingOutside, "file"))},
		{"dangling_symlink_inside", cwd, request("allow_once", filepath.Join(danglingInside, "file"))},
		{"dot-dot escape", cwd, request("allow_once", dotdot)},
		{"dot_dot_component", cwd, request("allow_once", dotDotComponent)},
		{"no allow_once", cwd, onlyAlways},
		{"empty options", cwd, request("", inside)},
		{"prefix sibling", work, request("allow_once", filepath.Join(evil, "file"))},
		{"mixed inside and outside", cwd, request("allow_once", inside, filepath.Join(outside, "file"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := WorktreePermissionPolicy(context.Background(), Execution{Request: Request{Cwd: tc.cwd}}, tc.request)
			if got != "" {
				t.Fatalf("got %q, want reject", got)
			}
		})
	}
}

func TestWorktreeLocationHasDotDot(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		path string
		want bool
	}{
		{"dot dot between slashes", "dir/missing/../file.txt", true},
		{"dot dot between platform separators", "dir" + string(filepath.Separator) + ".." + string(filepath.Separator) + "file.txt", true},
		{"leading dot dot", ".." + string(filepath.Separator) + "file.txt", true},
		{"partial element", filepath.Join("dir", "..etc", "file.txt"), false},
		{"no dot dot", filepath.Join("dir", "file.txt"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := worktreeLocationHasDotDot(tc.path); got != tc.want {
				t.Fatalf("worktreeLocationHasDotDot(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

func resolvedCwd(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func permissionLocations(paths ...string) []struct {
	Path string `json:"path"`
} {
	locations := make([]struct {
		Path string `json:"path"`
	}, len(paths))
	for i, path := range paths {
		locations[i].Path = path
	}
	return locations
}
