package executor

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

var streamDecoderNames = []string{
	"cursor-stream-json",
	"agy-stream-json",
	"codex-json",
	"claude-stream-json",
	"opencode-json",
}

func TestDecoderLookup(t *testing.T) {
	t.Parallel()
	for _, name := range streamDecoderNames {
		if LookupDecoder(name) == nil {
			t.Fatalf("LookupDecoder(%q) = nil", name)
		}
	}
}

func TestDecodersReplayFixtures(t *testing.T) {
	t.Parallel()
	for _, name := range streamDecoderNames {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			decoder := LookupDecoder(name)
			if decoder == nil {
				t.Fatalf("LookupDecoder(%q) = nil", name)
			}
			payload, err := os.ReadFile(filepath.Join("testdata", "stream", name+".jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			var text bytes.Buffer
			scanner := bufio.NewScanner(bytes.NewReader(payload))
			for scanner.Scan() {
				text.WriteString(decoder.Decode(scanner.Text()))
			}
			if err := scanner.Err(); err != nil {
				t.Fatal(err)
			}
			wantPayload, err := os.ReadFile(filepath.Join("testdata", "stream", name+".want.json"))
			if err != nil {
				t.Fatal(err)
			}
			var want struct {
				Text  string `json:"text"`
				Usage *Usage `json:"usage"`
			}
			if err := json.Unmarshal(wantPayload, &want); err != nil {
				t.Fatal(err)
			}
			if text.String() != want.Text {
				t.Fatalf("text = %q, want %q", text.String(), want.Text)
			}
			if !reflect.DeepEqual(decoder.Usage(), want.Usage) {
				t.Fatalf("usage = %#v, want %#v", decoder.Usage(), want.Usage)
			}
		})
	}
}

func TestDecoderUnknownLines(t *testing.T) {
	t.Parallel()
	for _, name := range streamDecoderNames {
		decoder := LookupDecoder(name)
		if decoder == nil {
			t.Fatalf("LookupDecoder(%q) = nil", name)
		}
		if got := decoder.Decode("not json"); got != "not json" {
			t.Fatalf("%s passthrough = %q, want %q", name, got, "not json")
		}
		if got := decoder.Decode(`{"type":"not-an-event","event":"nope"}`); got != "" {
			t.Fatalf("%s unknown event = %q, want empty", name, got)
		}
	}
}
