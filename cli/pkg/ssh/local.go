package ssh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// LocalRunner executes commands locally (for localhost deployments)
type LocalRunner struct {
	workDir string
}

// NewLocalRunner creates a runner that executes commands locally
func NewLocalRunner(workDir string) *LocalRunner {
	return &LocalRunner{
		workDir: workDir,
	}
}

// Run executes a command locally
func (l *LocalRunner) Run(ctx context.Context, command string) (*CommandResult, error) {
	result := &CommandResult{
		Command: command,
	}

	start := time.Now()
	defer func() {
		result.Duration = time.Since(start)
	}()

	// Create command
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	if l.workDir != "" {
		cmd.Dir = l.workDir
	}

	// Capture output
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		result.Error = fmt.Errorf("failed to create stdout pipe: %w", err)
		return result, result.Error
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		result.Error = fmt.Errorf("failed to create stderr pipe: %w", err)
		return result, result.Error
	}

	// Start command
	if err := cmd.Start(); err != nil {
		result.Error = fmt.Errorf("failed to start command: %w", err)
		result.ExitCode = -1
		return result, result.Error
	}

	// Read output
	stdoutBytes, _ := io.ReadAll(stdout)
	stderrBytes, _ := io.ReadAll(stderr)

	result.Stdout = strings.TrimSpace(string(stdoutBytes))
	result.Stderr = strings.TrimSpace(string(stderrBytes))

	// Wait for completion
	if err := cmd.Wait(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		} else {
			result.ExitCode = -1
		}
		result.Error = err
		return result, err
	}

	result.ExitCode = 0
	return result, nil
}

// RunScript executes a shell script locally
func (l *LocalRunner) RunScript(ctx context.Context, script string) (*CommandResult, error) {
	// Write script to temp file
	tempFile, err := os.CreateTemp("", "frameworks-script-*.sh")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}
	defer os.Remove(tempFile.Name())

	if _, err := tempFile.WriteString(script); err != nil {
		return nil, fmt.Errorf("failed to write script: %w", err)
	}

	if err := tempFile.Chmod(0700); err != nil {
		return nil, fmt.Errorf("failed to chmod script: %w", err)
	}

	if err := tempFile.Close(); err != nil {
		return nil, fmt.Errorf("failed to close temp file: %w", err)
	}

	// Execute script
	return l.Run(ctx, tempFile.Name())
}

// Upload copies a file locally
func (l *LocalRunner) Upload(ctx context.Context, opts UploadOptions) error {
	// Create directory if needed
	remoteDir := strings.TrimSuffix(opts.RemotePath, "/"+strings.Split(opts.RemotePath, "/")[len(strings.Split(opts.RemotePath, "/"))-1])
	if err := os.MkdirAll(remoteDir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Copy file
	data, err := os.ReadFile(opts.LocalPath)
	if err != nil {
		return fmt.Errorf("failed to read source file: %w", err)
	}

	mode := opts.Mode
	if mode == 0 {
		mode = 0644
	}

	if err := os.WriteFile(opts.RemotePath, data, os.FileMode(mode)); err != nil {
		return fmt.Errorf("failed to write destination file: %w", err)
	}

	// Change ownership if specified (requires sudo)
	if opts.Owner != "" {
		chownCmd := fmt.Sprintf("sudo chown %s %s", ShellQuote(opts.Owner), ShellQuote(opts.RemotePath))
		if opts.Group != "" {
			chownCmd = fmt.Sprintf("sudo chown %s:%s %s", ShellQuote(opts.Owner), ShellQuote(opts.Group), ShellQuote(opts.RemotePath))
		}
		if _, err := l.Run(ctx, chownCmd); err != nil {
			return fmt.Errorf("failed to change ownership: %w", err)
		}
	}

	return nil
}

// Close is a no-op for local runner
func (l *LocalRunner) Close() error {
	return nil
}
