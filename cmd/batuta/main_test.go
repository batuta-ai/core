package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunRequiresASubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run(nil, &stdout, &stderr); err == nil {
		t.Fatal("run() with no arguments should fail")
	}
	if !strings.Contains(stderr.String(), "Usage:") {
		t.Fatalf("stderr = %q, want usage", stderr.String())
	}
}

func TestRunRejectsUnknownSubcommands(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"conduct"}, &stdout, &stderr); err == nil || !strings.Contains(err.Error(), "conduct") {
		t.Fatalf("run(conduct) error = %v, want unknown subcommand", err)
	}
}

func TestRunPrintsVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"version"}, &stdout, &stderr); err != nil {
		t.Fatalf("run(version) error = %v", err)
	}
	if strings.TrimSpace(stdout.String()) == "" {
		t.Fatal("version printed nothing")
	}
}
