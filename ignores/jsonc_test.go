package ignores

import (
	"reflect"
	"testing"
)

func TestParseJSONC(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   string
		want    any
		wantErr bool
	}{
		{name: "plain object", input: `{"a": 1}`, want: map[string]any{"a": 1.0}},
		{name: "leading BOM", input: "\xef\xbb\xbf{\"a\": true}", want: map[string]any{"a": true}},
		{name: "line comment", input: "{\n// note\n\"a\": 1 // tail\n}", want: map[string]any{"a": 1.0}},
		{name: "block comment", input: `{/* one
two */"a": 1}`, want: map[string]any{"a": 1.0}},
		{name: "slashes inside string", input: `{"a": "http://x/*y*/"}`, want: map[string]any{"a": "http://x/*y*/"}},
		{name: "escaped quote in string", input: `{"a": "q\" // not a comment"}`, want: map[string]any{"a": `q" // not a comment`}},
		{name: "trailing comma in object", input: `{"a": 1,}`, want: map[string]any{"a": 1.0}},
		{name: "trailing comma in array", input: `[1, 2,]`, want: []any{1.0, 2.0}},
		{name: "trailing comma before comment", input: "[1,\n// last\n]", want: []any{1.0}},
		{name: "nested trailing commas", input: `{"a": [1, {"b": 2,},],}`, want: map[string]any{"a": []any{1.0, map[string]any{"b": 2.0}}}},
		{name: "comma inside string kept", input: `{"a": ",}"}`, want: map[string]any{"a": ",}"}},
		{name: "unterminated string", input: `{"a": "open}`, wantErr: true},
		{name: "unterminated string at end", input: `{"a": "open`, wantErr: true},
		{name: "unterminated block comment", input: `{"a": 1 /* open`, wantErr: true},
		{name: "invalid JSON", input: `{"a": }`, wantErr: true},
		{name: "empty input", input: ``, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseJSONC([]byte(tt.input))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseJSONC(%q) = %v, want an error", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseJSONC(%q): %v", tt.input, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseJSONC(%q) = %#v, want %#v", tt.input, got, tt.want)
			}
		})
	}
}
