package provisioner

import (
	"context"
	"os"
	"time"

	"frameworks/cli/pkg/ssh"
)

// mockRunner is a test double for ssh.Runner that records commands and uploads.
type mockRunner struct {
	stdout          string
	stderr          string
	exitCode        int
	lastCmd         string
	lastUpload      ssh.UploadOptions
	uploadedContent string // content of the last uploaded file
}

func (m *mockRunner) Run(_ context.Context, command string) (*ssh.CommandResult, error) {
	m.lastCmd = command
	return &ssh.CommandResult{
		Command:  command,
		ExitCode: m.exitCode,
		Stdout:   m.stdout,
		Stderr:   m.stderr,
		Duration: time.Millisecond,
	}, nil
}

func (m *mockRunner) RunScript(_ context.Context, script string) (*ssh.CommandResult, error) {
	m.lastCmd = script
	return &ssh.CommandResult{
		Command:  script,
		ExitCode: m.exitCode,
		Stdout:   m.stdout,
		Stderr:   m.stderr,
		Duration: time.Millisecond,
	}, nil
}

func (m *mockRunner) Upload(_ context.Context, opts ssh.UploadOptions) error {
	m.lastUpload = opts
	data, err := os.ReadFile(opts.LocalPath)
	if err != nil {
		return err
	}
	m.uploadedContent = string(data)
	return nil
}

func (m *mockRunner) Close() error {
	return nil
}
