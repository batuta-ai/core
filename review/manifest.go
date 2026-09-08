// Package review accounts for delivery changes and derives review results mechanically.
package review

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	CohortFiles = 8
	CohortLines = 1200
)

type Hunk struct {
	Start int `json:"start"`
	Count int `json:"count"`
}

type File struct {
	Path         string `json:"path"`
	OldPath      string `json:"old_path,omitempty"`
	Status       string `json:"status"`
	Untracked    bool   `json:"untracked,omitempty"`
	Hunks        []Hunk `json:"hunks"`
	Added        int    `json:"added"`
	Deleted      int    `json:"deleted"`
	Binary       bool   `json:"binary,omitempty"`
	Ignored      bool   `json:"ignored"`
	IgnoreReason string `json:"ignore_reason,omitempty"`
	Selected     bool   `json:"selected"`
}

type Cohort struct {
	Files        []string `json:"files"`
	ChangedLines int      `json:"changed_lines"`
}

type Manifest struct {
	Base    string   `json:"base"`
	Files   []File   `json:"files"`
	Cohorts []Cohort `json:"cohorts"`
}

type ManifestOptions struct {
	Worktree    bool
	CohortFiles int // Zero uses CohortFiles.
}

// BuildManifest compares the tracked working tree (including staged changes) to
// base. Worktree additionally includes untracked, non-gitignored files. A nonempty
// files list selects exact repository-relative paths without hiding other changes.
// The returned cohorts serve both defect and polish reviewers.
func BuildManifest(root, base string, files []string, options ...ManifestOptions) (Manifest, error) {
	var manifest Manifest
	if len(options) > 1 {
		return manifest, fmt.Errorf("review: expected at most one manifest options value")
	}
	opts := ManifestOptions{CohortFiles: CohortFiles}
	if len(options) == 1 {
		opts = options[0]
		if opts.CohortFiles == 0 {
			opts.CohortFiles = CohortFiles
		}
	}
	if opts.CohortFiles < 1 || strings.TrimSpace(base) == "" {
		return manifest, fmt.Errorf("review: a base and positive cohort file limit are required")
	}
	selection := make(map[string]bool, len(files))
	for _, name := range files {
		if !validPath(name) {
			return manifest, fmt.Errorf("review: invalid selected path %q", name)
		}
		selection[path.Clean(name)] = false
	}
	resolved, err := gitOutput(root, "rev-parse", "--verify", "--end-of-options", base+"^{commit}")
	if err != nil {
		return manifest, err
	}
	manifest.Base = strings.TrimSpace(string(resolved))
	status, err := gitOutput(root, "diff", "--name-status", "-z", "--find-renames", "--no-relative", "--ignore-submodules=none", manifest.Base, "--")
	if err != nil {
		return manifest, err
	}
	manifest.Files, err = parseNameStatus(status)
	if err != nil {
		return manifest, err
	}
	if opts.Worktree {
		untracked, err := gitOutput(root, "ls-files", "--others", "--exclude-standard", "-z")
		if err != nil {
			return manifest, err
		}
		byPath := make(map[string]int, len(manifest.Files))
		for i, file := range manifest.Files {
			byPath[file.Path] = i
		}
		for _, name := range bytes.Split(untracked, []byte{0}) {
			if len(name) > 0 {
				if i, exists := byPath[string(name)]; exists {
					manifest.Files[i].Untracked = true
				} else {
					manifest.Files = append(manifest.Files, File{Path: string(name), Status: "?", Untracked: true})
				}
			}
		}
	}
	slices.SortFunc(manifest.Files, func(a, b File) int { return strings.Compare(a.Path, b.Path) })
	workspace, err := os.OpenRoot(root)
	if err != nil {
		return manifest, fmt.Errorf("review: open root: %w", err)
	}
	defer workspace.Close()
	for i := range manifest.Files {
		file := &manifest.Files[i]
		if err := inspectFile(root, workspace, manifest.Base, file); err != nil {
			return manifest, err
		}
		_, requested := selection[file.Path]
		if requested {
			selection[file.Path] = true
		}
		file.Selected = !file.Ignored && (len(selection) == 0 || requested)
	}
	for _, name := range files {
		if !selection[path.Clean(name)] {
			return manifest, fmt.Errorf("review: selected file %q is not changed", name)
		}
	}
	manifest.Cohorts, err = buildCohorts(manifest.Files, opts.CohortFiles)
	return manifest, err
}

func gitOutput(root string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"--literal-pathspecs", "-C", root}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("review: git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return output, nil
}

func parseNameStatus(data []byte) ([]File, error) {
	var files []File
	fields := bytes.Split(data, []byte{0})
	for i := 0; i < len(fields)-1; {
		status := string(fields[i])
		if status == "" || i+1 >= len(fields)-1 {
			return nil, fmt.Errorf("review: malformed git name-status record")
		}
		file := File{Status: status, Path: string(fields[i+1])}
		i += 2
		if status[0] == 'R' || status[0] == 'C' {
			if i >= len(fields)-1 {
				return nil, fmt.Errorf("review: incomplete git rename/copy record")
			}
			file.OldPath, file.Path = file.Path, string(fields[i])
			i++
		}
		if status[0] == 'U' {
			return nil, fmt.Errorf("review: unresolved conflict in %q", file.Path)
		}
		files = append(files, file)
	}
	return files, nil
}

func inspectFile(root string, workspace *os.Root, base string, file *File) error {
	if file.Status != "?" {
		args := []string{"diff", "-U0", "--inter-hunk-context=0", "--no-color", "--no-ext-diff", "--no-textconv", "--find-renames", "--no-relative", "--ignore-submodules=none", "--submodule=short", base, "--", file.Path}
		if file.OldPath != "" {
			args = append(args, file.OldPath)
		}
		diff, err := gitOutput(root, args...)
		if err != nil {
			return err
		}
		if err := parseDiff(diff, file); err != nil {
			return err
		}
	}
	var content []byte
	var err error
	if file.Untracked {
		content, err = workspaceContent(workspace, file.Path, true)
		if err != nil {
			return fmt.Errorf("review: read %q: %w", file.Path, err)
		}
		binary := bytes.IndexByte(content, 0) >= 0
		file.Binary = file.Binary || binary
		if !binary && len(content) > 0 {
			added := bytes.Count(content, []byte{'\n'})
			if content[len(content)-1] != '\n' {
				added++
			}
			file.Added += added
			file.Hunks = append(file.Hunks, Hunk{Start: 1, Count: added})
		}
	} else if file.Status == "D" {
		var entry []byte
		entry, err = gitOutput(root, "ls-tree", "-z", base, "--", file.Path)
		if err == nil && !bytes.HasPrefix(entry, []byte("160000 ")) {
			content, err = gitOutput(root, "show", base+":"+file.Path)
		}
	} else {
		content, err = workspaceContent(workspace, file.Path, false)
	}
	if err != nil {
		return fmt.Errorf("review: read %q: %w", file.Path, err)
	}
	file.IgnoreReason = ignoreReason(file.Path, content)
	file.Ignored = file.IgnoreReason != ""
	return nil
}

func workspaceContent(root *os.Root, name string, whole bool) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := root.Readlink(name)
		return []byte(target), err
	}
	if info.IsDir() {
		return nil, nil // A tracked gitlink has no source header.
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if whole {
		return io.ReadAll(file)
	}
	line, err := bufio.NewReader(file).ReadBytes('\n')
	if err == io.EOF {
		err = nil
	}
	return line, err
}

var hunkHeader = regexp.MustCompile(`^@@ -[0-9]+(?:,([0-9]+))? \+([0-9]+)(?:,([0-9]+))? @@`)

func parseDiff(diff []byte, file *File) error {
	for _, line := range strings.Split(string(diff), "\n") {
		if strings.HasPrefix(line, "Binary files ") || line == "GIT binary patch" {
			file.Binary = true
		}
		if !strings.HasPrefix(line, "@@") {
			continue
		}
		match := hunkHeader.FindStringSubmatch(line)
		if match == nil {
			return fmt.Errorf("review: malformed hunk for %q", file.Path)
		}
		values := []int{1, 0, 1}
		for i, text := range match[1:] {
			if text != "" {
				value, err := strconv.Atoi(text)
				if err != nil {
					return fmt.Errorf("review: hunk range for %q: %w", file.Path, err)
				}
				values[i] = value
			}
		}
		file.Hunks = append(file.Hunks, Hunk{Start: values[1], Count: values[2]})
		file.Deleted += values[0]
		file.Added += values[2]
	}
	return nil
}

func ignoreReason(name string, content []byte) string {
	parts := strings.Split(name, "/")
	for _, part := range parts[:len(parts)-1] {
		if part == "vendor" || part == "node_modules" {
			return "vendored"
		}
	}
	base := path.Base(name)
	if base == "go.sum" || strings.HasSuffix(base, ".lock") {
		return "lock"
	}
	for _, part := range parts[:len(parts)-1] {
		if part == "dist" || part == "build" {
			return "generated"
		}
	}
	first, _, _ := bytes.Cut(content, []byte{'\n'})
	if strings.HasSuffix(base, ".pb.go") || strings.HasSuffix(base, "_generated.go") || strings.Contains(base, ".min.") || bytes.Contains(first, []byte("Code generated")) || bytes.Contains(first, []byte("DO NOT EDIT")) {
		return "generated"
	}
	return ""
}

func buildCohorts(files []File, limit int) ([]Cohort, error) {
	var cohorts []Cohort
	for _, file := range files {
		if !file.Selected {
			continue
		}
		lines := file.Added + file.Deleted
		if lines > CohortLines {
			return nil, fmt.Errorf("review: %q has %d changed lines, exceeding the indivisible cohort limit of %d", file.Path, lines, CohortLines)
		}
		if len(cohorts) == 0 || len(cohorts[len(cohorts)-1].Files) == limit || cohorts[len(cohorts)-1].ChangedLines+lines > CohortLines {
			cohorts = append(cohorts, Cohort{})
		}
		cohort := &cohorts[len(cohorts)-1]
		cohort.Files = append(cohort.Files, file.Path)
		cohort.ChangedLines += lines
	}
	return cohorts, nil
}

func validPath(name string) bool {
	clean := path.Clean(name)
	return strings.TrimSpace(name) != "" && !path.IsAbs(name) && !filepath.IsAbs(name) && clean != "." && clean != ".." && !strings.HasPrefix(clean, "../") && !strings.ContainsAny(name, "\\\x00") && !(len(name) > 1 && name[1] == ':')
}
