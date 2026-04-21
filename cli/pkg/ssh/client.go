package ssh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var execCommandContext = exec.CommandContext

// Client runs commands on a remote host by invoking the system `ssh` and
// `scp` binaries, so operator ~/.ssh/config, ssh-agent, default identities,
// ProxyJump, ProxyCommand, macOS keychain, and multiplexing all apply.
// There is no persistent TCP connection — each Run/Upload/Ping spawns ssh.
type Client struct {
	config     *ConnectionConfig
	resolution Resolution
	resolver   Resolver
	pingFunc   func(ctx context.Context) error
}

// NewClient resolves the ssh target once (including alias verification via
// ssh -G when HostName is set) and caches the resolution. No TCP connection
// is opened here.
func NewClient(config *ConnectionConfig) (*Client, error) {
	if config == nil {
		return nil, errors.New("nil ConnectionConfig")
	}
	if config.Address == "" {
		return nil, errors.New("ConnectionConfig.Address is required")
	}
	if config.Port == 0 {
		config.Port = 22
	}
	resolver := &DefaultResolver{}
	ctx, cancel := context.WithTimeout(context.Background(), resolveTimeout(config))
	defer cancel()
	res, err := resolver.Resolve(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("resolve ssh target: %w", err)
	}
	return &Client{
		config:     config,
		resolution: res,
		resolver:   resolver,
	}, nil
}

// Run executes a command on the remote host. The returned error is non-nil
// for both non-zero exit codes and ssh-process failures; callers that need to
// distinguish the two should read result.ExitCode.
func (c *Client) Run(ctx context.Context, command string) (*CommandResult, error) {
	result := &CommandResult{Command: command}
	start := time.Now()
	defer func() { result.Duration = time.Since(start) }()

	args := BuildSSHArgs(c.config, c.resolution)
	// OpenSSH joins argv after the target with single spaces and ships that as
	// one string to the remote login shell. Passing command as its own argv
	// element would be re-split: `sh -c mkdir -p /path` makes -c consume only
	// `mkdir` and the rest become positional parameters. ShellQuote keeps it
	// as a single token across the wire. `sh -c` (not `-lc`) stays portable
	// across dash, ash, and BusyBox sh.
	args = append(args, c.resolution.Target, "sh", "-c", ShellQuote(command))

	cmd := execCommandContext(ctx, "ssh", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	fmt.Fprintf(os.Stderr, "  Connecting to %s...\n", c.resolution.Target)
	err := cmd.Run()
	result.Stdout = strings.TrimSpace(stdout.String())
	result.Stderr = strings.TrimSpace(stderr.String())

	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		} else {
			result.ExitCode = -1
		}
		result.Error = err
		return result, wrapRunError(c.resolution.Target, command, result.ExitCode, result.Stderr, err)
	}

	result.ExitCode = 0
	return result, nil
}

// wrapRunError builds a readable error from a failed ssh command execution.
// Cause is wrapped with %w so callers can still reach *exec.ExitError via
// errors.As. Stderr is capped so a rogue command can't bloat error chains.
func wrapRunError(target, command string, exitCode int, stderr string, cause error) error {
	const stderrCap = 2048
	stderr = strings.TrimSpace(stderr)
	if len(stderr) > stderrCap {
		stderr = stderr[:stderrCap] + "… [truncated]"
	}

	if exitCode > 0 {
		if stderr == "" {
			return fmt.Errorf("ssh %s: %q exited %d (no stderr): %w", target, command, exitCode, cause)
		}
		return fmt.Errorf("ssh %s: %q exited %d: %s: %w", target, command, exitCode, stderr, cause)
	}
	if stderr == "" {
		return fmt.Errorf("ssh %s: %q: %w", target, command, cause)
	}
	return fmt.Errorf("ssh %s: %q: %s: %w", target, command, stderr, cause)
}

// Ping validates that ssh can reach the host.
func (c *Client) Ping(ctx context.Context) error {
	if c.pingFunc != nil {
		return c.pingFunc(ctx)
	}
	pingCtx, cancel := context.WithTimeout(ctx, pingTimeout(c.config))
	defer cancel()

	args := BuildSSHArgs(c.config, c.resolution)
	args = append(args, c.resolution.Target, "true")
	cmd := execCommandContext(pingCtx, "ssh", args...)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ssh ping failed: %w", err)
	}
	return nil
}

// resolveTimeout bounds alias verification + DNS lookups. Uses the caller's
// Timeout when set so slow-link operators don't get artificially short budgets.
func resolveTimeout(c *ConnectionConfig) time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return 5 * time.Second
}

// pingTimeout bounds the ssh liveness probe.
func pingTimeout(c *ConnectionConfig) time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return 5 * time.Second
}

// RunScript writes the script to a local temp file, scps it to /tmp on the
// remote, executes it, and removes it.
func (c *Client) RunScript(ctx context.Context, script string) (*CommandResult, error) {
	remotePath := fmt.Sprintf("/tmp/frameworks-script-%d.sh", time.Now().UnixNano())

	localTemp := filepath.Join(os.TempDir(), filepath.Base(remotePath))
	if err := os.WriteFile(localTemp, []byte(script), 0700); err != nil {
		return nil, fmt.Errorf("failed to write script to temp file: %w", err)
	}
	defer os.Remove(localTemp)

	if err := c.Upload(ctx, UploadOptions{
		LocalPath:  localTemp,
		RemotePath: remotePath,
		Mode:       0700,
	}); err != nil {
		return nil, fmt.Errorf("failed to upload script: %w", err)
	}

	result, err := c.Run(ctx, remotePath)

	_, _ = c.Run(ctx, fmt.Sprintf("rm -f %s", ShellQuote(remotePath))) //nolint:errcheck // best-effort cleanup

	return result, err
}

// Upload transfers a file via scp.
func (c *Client) Upload(ctx context.Context, opts UploadOptions) error {
	remoteDir := filepath.Dir(opts.RemotePath)
	if _, err := c.Run(ctx, fmt.Sprintf("mkdir -p %s", ShellQuote(remoteDir))); err != nil {
		return fmt.Errorf("prepare remote directory %s: %w", remoteDir, err)
	}

	scpArgs := BuildSCPArgs(c.config, c.resolution, opts.LocalPath, opts.RemotePath)
	cmd := execCommandContext(ctx, "scp", scpArgs...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		exitCode := -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
		return wrapScpError(c.resolution.Target, opts.LocalPath, opts.RemotePath, exitCode, stderr.String(), err)
	}

	// scp preserves source mode by default; explicit chmod lets callers enforce
	// a specific one (e.g. 0600 for secrets regardless of local umask).
	if opts.Mode != 0 {
		chmodCmd := fmt.Sprintf("chmod %o %s", opts.Mode, ShellQuote(opts.RemotePath))
		if _, err := c.Run(ctx, chmodCmd); err != nil {
			return fmt.Errorf("chmod %o on %s: %w", opts.Mode, opts.RemotePath, err)
		}
	}

	if opts.Owner != "" {
		chownCmd := fmt.Sprintf("chown %s %s", ShellQuote(opts.Owner), ShellQuote(opts.RemotePath))
		ownerSpec := opts.Owner
		if opts.Group != "" {
			chownCmd = fmt.Sprintf("chown %s:%s %s", ShellQuote(opts.Owner), ShellQuote(opts.Group), ShellQuote(opts.RemotePath))
			ownerSpec = opts.Owner + ":" + opts.Group
		}
		if _, err := c.Run(ctx, chownCmd); err != nil {
			return fmt.Errorf("chown %s on %s: %w", ownerSpec, opts.RemotePath, err)
		}
	}

	return nil
}

// wrapScpError mirrors wrapRunError for scp failures: exit code, target,
// paths, and trimmed stderr in a single readable message.
func wrapScpError(target, local, remote string, exitCode int, stderr string, cause error) error {
	const stderrCap = 2048
	stderr = strings.TrimSpace(stderr)
	if len(stderr) > stderrCap {
		stderr = stderr[:stderrCap] + "… [truncated]"
	}
	if exitCode > 0 {
		if stderr == "" {
			return fmt.Errorf("scp %s → %s:%s exited %d (no stderr): %w", local, target, remote, exitCode, cause)
		}
		return fmt.Errorf("scp %s → %s:%s exited %d: %s: %w", local, target, remote, exitCode, stderr, cause)
	}
	if stderr == "" {
		return fmt.Errorf("scp %s → %s:%s: %w", local, target, remote, cause)
	}
	return fmt.Errorf("scp %s → %s:%s: %s: %w", local, target, remote, stderr, cause)
}

func (c *Client) Close() error {
	return nil
}
