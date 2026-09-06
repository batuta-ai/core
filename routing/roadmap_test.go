package routing

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const roadmapFixture = `# Roadmap — Checkout delivery

Each phase is approved independently.
- [x] 1. Lay the foundation → plans/foundation.md
- [ ] 2. Harden checkout → plans/checkout-hardening.md
Additional prose.
- [ ] 3. Release
`

func TestParseRoadmapReadsPhasesInOrder(t *testing.T) {
	t.Parallel()
	for _, payload := range []string{roadmapFixture, strings.ReplaceAll(roadmapFixture, "\n", "\r\n")} {
		roadmap, err := ParseRoadmap([]byte(payload))
		if err != nil {
			t.Fatalf("ParseRoadmap() error = %v", err)
		}
		want := []RoadmapPhase{
			{Number: 1, Title: "Lay the foundation", Slug: "foundation", Done: true},
			{Number: 2, Title: "Harden checkout", Slug: "checkout-hardening"},
			{Number: 3, Title: "Release"},
		}
		if roadmap.Title != "Checkout delivery" || !reflect.DeepEqual(roadmap.Phases, want) {
			t.Fatalf("ParseRoadmap() = %#v, want title Checkout delivery and phases %#v", roadmap, want)
		}
	}
}

func TestParseRoadmapRejectsBrokenContractsWithTheLine(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		payload string
		line    int
	}{
		{"empty file", "", 1},
		{"missing title", "Prose\n- [ ] 1. Phase", 1},
		{"blank first line", "\n# Roadmap — Delivery", 1},
		{"empty title", "# Roadmap —   \n", 1},
		{"wrong heading", "# Plan — Delivery\n", 1},
		{"starts at zero", "# Roadmap — Delivery\n- [ ] 0. Phase", 2},
		{"starts at two", "# Roadmap — Delivery\n- [ ] 2. Phase", 2},
		{"duplicate", "# Roadmap — Delivery\n- [ ] 1. Phase\n- [x] 1. Again", 3},
		{"decreasing", "# Roadmap — Delivery\n- [ ] 1. Phase\n- [ ] 2. Next\n- [ ] 1. Earlier", 4},
		{"gap", "# Roadmap — Delivery\n- [ ] 1. Phase\n- [ ] 3. Next", 3},
		{"overflow", "# Roadmap — Delivery\n- [ ] 99999999999999999999999999. Phase", 2},
		{"outside plans", "# Roadmap — Delivery\n- [ ] 1. Phase → elsewhere/phase.md", 2},
		{"traversal", "# Roadmap — Delivery\n- [ ] 1. Phase → plans/../phase.md", 2},
		{"nested slug", "# Roadmap — Delivery\n- [ ] 1. Phase → plans/done/phase.md", 2},
		{"absolute path", "# Roadmap — Delivery\n- [ ] 1. Phase → /plans/phase.md", 2},
		{"missing extension", "# Roadmap — Delivery\n- [ ] 1. Phase → plans/phase", 2},
		{"empty slug", "# Roadmap — Delivery\n- [ ] 1. Phase → plans/.md", 2},
		{"empty phase title", "# Roadmap — Delivery\n- [ ] 1. → plans/phase.md", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseRoadmap([]byte(tc.payload))
			if !errors.Is(err, ErrReauthoringRequired) || !strings.Contains(err.Error(), fmt.Sprintf("line %d:", tc.line)) {
				t.Fatalf("error = %v, want ErrReauthoringRequired at line %d", err, tc.line)
			}
		})
	}
}

func TestRoadmapLoaderReportsMissingPlans(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		files []string
		want  []bool
	}{
		{"no plans directory", nil, []bool{true, true, true}},
		{"active and archived", []string{"plans/done/foundation.md", "plans/checkout-hardening.md"}, []bool{false, false, true}},
		{"both directories", []string{"plans/foundation.md", "plans/done/foundation.md"}, []bool{false, true, true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for _, name := range append([]string{"roadmap.md"}, tc.files...) {
				path := filepath.Join(root, ".batuta", filepath.FromSlash(name))
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(roadmapFixture), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			loader, err := NewRoadmapLoader(root)
			if err != nil {
				t.Fatalf("NewRoadmapLoader() error = %v", err)
			}
			roadmap, err := loader.Load()
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if roadmap.Title != "Checkout delivery" || len(roadmap.Phases) != len(tc.want) {
				t.Fatalf("Load() = %#v", roadmap)
			}
			for i, missing := range tc.want {
				if roadmap.Phases[i].Missing != missing {
					t.Errorf("phase %d Missing = %v, want %v", i+1, roadmap.Phases[i].Missing, missing)
				}
			}
		})
	}
}

func TestTickPhaseRewritesOnlyTheLine(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "roadmap.md")
	before := "# Roadmap — Checkout delivery\r\n\r\n" +
		"- [ ] 1. Foundation → plans/foundation.md\r\n" +
		"Keep plans/checkout-hardening.md in this prose.\r\n" +
		"- [ ] 2. Harden checkout → plans/checkout-hardening.md\r\n" +
		"- [ ] 3. Release → plans/release.md\r\n"
	if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := TickPhase(path, "checkout-hardening"); err != nil {
		t.Fatalf("TickPhase() error = %v", err)
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(before,
		"- [ ] 2. Harden checkout → plans/checkout-hardening.md",
		"- [x] 2. Harden checkout → plans/checkout-hardening.md", 1)
	if string(payload) != want {
		t.Fatalf("roadmap after TickPhase() = %q, want %q", payload, want)
	}
}
