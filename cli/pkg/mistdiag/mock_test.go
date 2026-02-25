package mistdiag

import (
	"context"
	"time"

	fwssh "frameworks/cli/pkg/ssh"
)

// mockRunner implements ssh.Runner for testing.
type mockRunner struct {
	stdout   string
	stderr   string
	exitCode int
	err      error
}

func (m *mockRunner) Run(_ context.Context, _ string) (*fwssh.CommandResult, error) {
	if m.err != nil {
		return nil, m.err
	}
	return &fwssh.CommandResult{
		Stdout:   m.stdout,
		Stderr:   m.stderr,
		ExitCode: m.exitCode,
		Duration: time.Millisecond,
	}, nil
}

func (m *mockRunner) RunScript(_ context.Context, _ string) (*fwssh.CommandResult, error) {
	return m.Run(context.Background(), "")
}

func (m *mockRunner) Upload(_ context.Context, _ fwssh.UploadOptions) error {
	return nil
}

func (m *mockRunner) Close() error {
	return nil
}
