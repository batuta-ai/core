package executor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/batuta-ai/core/executor/acp"
)

// NewNativeTransport wires the managed-group lifecycle without qualifying any
// provider. Release-owned evidence must populate exact Qualifications before
// native execution is eligible; adapter metadata cannot supply that evidence.
func NewNativeTransport(mode string) TransportBackend {
	return TransportBackend{
		Mode: mode,
		ACP: ACPBackend{
			Open: openNativeACP,
			PermissionPolicy: func(context.Context, Execution, acp.PermissionRequest) string {
				return ""
			},
		},
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
