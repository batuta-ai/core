package questions

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestUnitsSplit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		passage string
		want    []string
	}{
		{
			name:    "title only",
			passage: "Add the panel",
			want:    nil,
		},
		{
			name:    "scope and accept lines keep their prefix",
			passage: "Add the panel\nScope: loop/panel.go\nScope: loop/panel_test.go\nAccept: it renders; it scrolls ;; it quits\nAccept: ",
			want: []string{
				"Scope: loop/panel.go",
				"Scope: loop/panel_test.go",
				"Accept: it renders",
				"Accept: it scrolls",
				"Accept: it quits",
			},
		},
		{
			name:    "context splits on sentence ends and line breaks",
			passage: "T\nScope: a.go\n\nFirst one. Second one! Third one?\nFourth line\n\nFifth.",
			want: []string{
				"Scope: a.go",
				"First one.",
				"Second one!",
				"Third one?",
				"Fourth line",
				"Fifth.",
			},
		},
		{
			name:    "period inside a path does not split",
			passage: "T\n\nEdit loop/panel.go and v1.2 now.",
			want:    []string{"Edit loop/panel.go and v1.2 now."},
		},
		{
			name:    "semicolons in context do not split",
			passage: "T\n\nOne; two.",
			want:    []string{"One; two."},
		},
		{
			name:    "empty units are dropped",
			passage: "T\nScope:  \n\n  \nOnly.   \n",
			want:    []string{"Only."},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := Units(tt.passage); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Units() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestUnitsBound(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		passage string
		wantLen int
	}{
		{"ascii cut at 400", "T\n\n" + strings.Repeat("a", 500), 400},
		{"exactly 400 kept", "T\n\n" + strings.Repeat("a", 400), 400},
		{"multibyte cut at last complete character", "T\n\n" + strings.Repeat("é", 250), 400},
		{"cut inside a character backs up", "T\n\na" + strings.Repeat("é", 250), 399},
		{"accept prefix counts toward the bound", "T\nAccept: " + strings.Repeat("a", 500), 400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			units := Units(tt.passage)
			if len(units) != 1 {
				t.Fatalf("Units() = %d units, want 1", len(units))
			}
			if len(units[0]) != tt.wantLen {
				t.Fatalf("unit length = %d, want %d", len(units[0]), tt.wantLen)
			}
			if !utf8.ValidString(units[0]) {
				t.Fatalf("unit is not valid UTF-8")
			}
		})
	}
}

func TestUnitPacket(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		passage string
		unit    string
		want    string
	}{
		{"title and unit", "Add the panel\nScope: a.go\n\nBody.", "Scope: a.go", "Add the panel\nScope: a.go"},
		{"single line passage", "Add the panel", "Body.", "Add the panel\nBody."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := UnitPacket(tt.passage, tt.unit); got != tt.want {
				t.Fatalf("UnitPacket() = %q, want %q", got, tt.want)
			}
		})
	}
}
