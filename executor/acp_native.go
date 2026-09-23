package executor

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/batuta-ai/core/executor/acp"
)

// NewNativeTransport wires the managed-group lifecycle and the exact OpenCode,
// Codex and Claude macOS arm64 qualifications supported by release-owned native
// evidence. Model "*" matches any requested model at launch matching;
// advertising and confirming the model are session checks after launch. Adapter
// metadata cannot qualify other launches; see docs/dispatch-measurement.md.
func NewNativeTransport(mode string) TransportBackend {
	return TransportBackend{
		Mode: mode,
		ACP: ACPBackend{
			Open:             openNativeACP,
			PermissionPolicy: WorktreePermissionPolicy,
		},
		Qualifications: []ACPQualification{{
			Executor: "opencode", Run: "opencode acp", Version: "1.18.31",
			GOOS: "darwin", GOARCH: "arm64", Model: "*", Effort: "",
			Permissions: true, Cleanup: true, AuthenticatedTask: true, Platform: true,
		}, {
			Executor: "codex", Run: "codex-acp", Version: "@agentclientprotocol/codex-acp 1.13.1",
			GOOS: "darwin", GOARCH: "arm64", Model: "*", Effort: "", Mode: "read-only",
			Permissions: true, Cleanup: true, AuthenticatedTask: true, Platform: true,
		}, {
			Executor: "claude", Run: "claude-agent-acp", Version: "0.81.1",
			GOOS: "darwin", GOARCH: "arm64", Model: "*", Effort: "", Mode: "acceptEdits",
			SessionMeta: json.RawMessage(`{"claudeCode":{"options":{"sandbox":{"enabled":true,"autoAllowBashIfSandboxed":true}}}}`),
			Permissions: true, Cleanup: true, AuthenticatedTask: true, Platform: true,
		}},
	}
}

func openNativeACP(ctx context.Context, execution Execution) (*acp.Connection, func() error, error) {
	invocation := execution.Invocation
	if !filepath.IsAbs(invocation.Executable) || !filepath.IsAbs(execution.Request.Cwd) || invocation.Dir != execution.Request.Cwd {
		return nil, nil, acp.ErrConfiguration
	}
	// TransportBackend supplies fixed, resolved argv. The process inherits the
	// environment; cancellation belongs to the protocol and owned shutdown.
	cmd := exec.Command(invocation.Executable, invocation.Args...)
	cmd.Dir = execution.Request.Cwd
	cmd.Env = os.Environ()
	process, err := acp.StartProcess(ctx, cmd, acp.Options{})
	if err != nil {
		return nil, nil, err
	}
	return process.Connection, process.Shutdown, nil
}
