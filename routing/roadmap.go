package routing

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type RoadmapPhase struct {
	Number  int
	Title   string
	Slug    string
	Done    bool
	Missing bool
}

type Roadmap struct {
	Title  string
	Phases []RoadmapPhase
}

type RoadmapLoader struct {
	root string
}

func NewRoadmapLoader(workspaceRoot string) (*RoadmapLoader, error) {
	loader, err := NewArtifactLoader(workspaceRoot)
	if err != nil {
		return nil, err
	}
	return &RoadmapLoader{root: loader.root}, nil
}

func (l *RoadmapLoader) Load() (Roadmap, error) {
	path, err := (&ArtifactLoader{root: l.root}).resolveContained(filepath.Join(".batuta", "roadmap.md"))
	if err != nil {
		return Roadmap{}, err
	}
	payload, err := readBoundedFile(path, maxTaskArtifactBytes)
	if err != nil {
		return Roadmap{}, err
	}
	roadmap, err := ParseRoadmap(payload)
	if err != nil {
		return Roadmap{}, err
	}
	for i := range roadmap.Phases {
		phase := &roadmap.Phases[i]
		phase.Missing = true
		if phase.Slug == "" {
			continue
		}
		for _, relative := range []string{
			PlanPath(phase.Slug),
			filepath.Join(".batuta", "plans", "done", phase.Slug+".md"),
		} {
			if _, err := os.Lstat(filepath.Join(l.root, relative)); errors.Is(err, os.ErrNotExist) {
				continue
			}
			path, err := (&ArtifactLoader{root: l.root}).resolveContained(relative)
			if err != nil {
				return Roadmap{}, err
			}
			info, err := os.Stat(path)
			if err != nil || !info.Mode().IsRegular() {
				return Roadmap{}, errors.New("routing: roadmap plan is unavailable")
			}
			phase.Missing = false
			break
		}
	}
	return roadmap, nil
}

var roadmapPhaseLine = regexp.MustCompile(`^- \[([ x])\]\s+([0-9]+)\.\s*(.*)$`)

// ParseRoadmap reads the title on line 1 and ordered phase entries; other
// lines are prose and do not become part of the roadmap.
func ParseRoadmap(payload []byte) (Roadmap, error) {
	if len(payload) > maxTaskArtifactBytes {
		return Roadmap{}, errors.New("routing: roadmap byte budget exceeded")
	}
	scanner := bufio.NewScanner(strings.NewReader(string(payload)))
	scanner.Buffer(make([]byte, 1024), maxTaskArtifactBytes)
	if !scanner.Scan() || !strings.HasPrefix(scanner.Text(), "# Roadmap — ") {
		return Roadmap{}, fmt.Errorf("%w: line 1: expected `# Roadmap — <title>`", ErrReauthoringRequired)
	}
	roadmap := Roadmap{Title: strings.TrimSpace(strings.TrimPrefix(scanner.Text(), "# Roadmap — "))}
	if roadmap.Title == "" {
		return Roadmap{}, fmt.Errorf("%w: line 1: expected `# Roadmap — <title>`", ErrReauthoringRequired)
	}
	lineNo := 1
	for scanner.Scan() {
		lineNo++
		m := roadmapPhaseLine.FindStringSubmatch(scanner.Text())
		if m == nil {
			continue
		}
		number, err := strconv.Atoi(m[2])
		if err != nil || number != len(roadmap.Phases)+1 {
			return Roadmap{}, fmt.Errorf("%w: line %d: phase numbers must start at 1 and increase by one", ErrReauthoringRequired, lineNo)
		}
		title, tail, linked := strings.Cut(m[3], "→")
		phase := RoadmapPhase{Number: number, Title: strings.TrimSpace(title), Done: m[1] == "x"}
		if phase.Title == "" {
			return Roadmap{}, fmt.Errorf("%w: line %d: phase title is required", ErrReauthoringRequired, lineNo)
		}
		if linked {
			tail = strings.TrimSpace(tail)
			phase.Slug = strings.TrimSuffix(strings.TrimPrefix(tail, "plans/"), ".md")
			if !canonicalSlug.MatchString(phase.Slug) || tail != "plans/"+phase.Slug+".md" {
				return Roadmap{}, fmt.Errorf("%w: line %d: phase plan must read `plans/<slug>.md`", ErrReauthoringRequired, lineNo)
			}
		}
		roadmap.Phases = append(roadmap.Phases, phase)
	}
	if err := scanner.Err(); err != nil {
		return Roadmap{}, err
	}
	return roadmap, nil
}
