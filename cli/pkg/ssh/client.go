package ssh

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

var (
	// knownHostsMu protects concurrent writes to known_hosts file
	knownHostsMu sync.Mutex
)

// Client wraps an SSH connection and implements Runner
type Client struct {
	config   *ConnectionConfig
	conn     *ssh.Client
	pingFunc func(ctx context.Context) error
}

// NewClient creates a new SSH client
func NewClient(config *ConnectionConfig) (*Client, error) {
	// Read private key
	key, err := os.ReadFile(config.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read private key: %w", err)
	}

	// Parse private key
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("failed to parse private key: %w", err)
	}

	// Build host key callback
	hostKeyCallback, err := buildHostKeyCallback(config)
	if err != nil {
		return nil, fmt.Errorf("failed to setup host key verification: %w", err)
	}

	// Build SSH client config
	sshConfig := &ssh.ClientConfig{
		User: config.User,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: hostKeyCallback,
		Timeout:         config.Timeout,
	}

	// Add password auth if provided
	if config.Password != "" {
		sshConfig.Auth = append(sshConfig.Auth, ssh.Password(config.Password))
	}

	// Connect
	addr := fmt.Sprintf("%s:%d", config.Address, config.Port)
	conn, err := ssh.Dial("tcp", addr, sshConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to dial SSH: %w", err)
	}

	return &Client{
		config: config,
		conn:   conn,
	}, nil
}

// buildHostKeyCallback creates a host key callback based on config
func buildHostKeyCallback(config *ConnectionConfig) (ssh.HostKeyCallback, error) {
	// If insecure mode, skip verification (with warning)
	if config.InsecureSkipVerify {
		fmt.Fprintf(os.Stderr, "WARNING: SSH host key verification disabled for %s - vulnerable to MITM attacks\n", config.Address)
		return ssh.InsecureIgnoreHostKey(), nil
	}

	// Determine known_hosts path
	knownHostsPath := config.KnownHostsPath
	if knownHostsPath == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		knownHostsPath = filepath.Join(homeDir, ".frameworks", "known_hosts")
	}

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(knownHostsPath), 0700); err != nil {
		return nil, fmt.Errorf("failed to create known_hosts directory: %w", err)
	}

	// Create empty known_hosts if it doesn't exist
	if _, err := os.Stat(knownHostsPath); os.IsNotExist(err) {
		if err := os.WriteFile(knownHostsPath, []byte{}, 0600); err != nil {
			return nil, fmt.Errorf("failed to create known_hosts file: %w", err)
		}
	}

	// Create TOFU (Trust On First Use) callback
	return createTOFUCallback(knownHostsPath, config.Address, config.Port), nil
}

// createTOFUCallback creates a Trust On First Use host key callback
func createTOFUCallback(knownHostsPath string, address string, port int) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		// Try to load existing known_hosts
		callback, err := knownhosts.New(knownHostsPath)
		if err != nil {
			// File might be empty or malformed, treat as new
			return trustAndSaveHostKey(knownHostsPath, hostname, remote, key)
		}

		// Check against known hosts
		err = callback(hostname, remote, key)
		if err == nil {
			// Host key matches
			return nil
		}

		// Check if it's a "key not found" error (new host)
		var keyErr *knownhosts.KeyError
		if !isKeyNotFoundError(err) {
			// Key mismatch - potential MITM attack!
			fingerprint := fingerprintSHA256(key)
			return fmt.Errorf("HOST KEY VERIFICATION FAILED for %s\n"+
				"  Someone may be doing a man-in-the-middle attack!\n"+
				"  The host key has changed.\n"+
				"  Key fingerprint: %s\n"+
				"  If this is expected (server reinstall), remove the old key from:\n"+
				"    %s\n"+
				"  Original error: %w",
				hostname, fingerprint, knownHostsPath, err)
		}
		_ = keyErr // suppress unused warning

		// New host - trust on first use
		return trustAndSaveHostKey(knownHostsPath, hostname, remote, key)
	}
}

// isKeyNotFoundError checks if the error indicates a new/unknown host
func isKeyNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	// knownhosts returns a KeyError with Want being empty for unknown hosts
	var keyErr *knownhosts.KeyError
	if ok := isKeyError(err, &keyErr); ok {
		return len(keyErr.Want) == 0
	}
	return false
}

// isKeyError checks if err is a KeyError and assigns it
func isKeyError(err error, target **knownhosts.KeyError) bool {
	var ke *knownhosts.KeyError
	if errors.As(err, &ke) {
		*target = ke
		return true
	}
	return false
}

// trustAndSaveHostKey saves a new host key to known_hosts (TOFU)
func trustAndSaveHostKey(knownHostsPath, hostname string, remote net.Addr, key ssh.PublicKey) error {
	knownHostsMu.Lock()
	defer knownHostsMu.Unlock()

	lockFile, err := lockKnownHosts(knownHostsPath)
	if err != nil {
		return fmt.Errorf("failed to lock known_hosts: %w", err)
	}
	defer unlockKnownHosts(lockFile)

	callback, err := knownhosts.New(knownHostsPath)
	if err == nil {
		if cbErr := callback(hostname, remote, key); cbErr == nil {
			return nil
		} else if !isKeyNotFoundError(cbErr) {
			return cbErr
		}
	}

	fingerprint := fingerprintSHA256(key)

	// Print warning about trusting new host
	fmt.Fprintf(os.Stderr, "Warning: Permanently adding '%s' to the list of known hosts.\n", hostname)
	fmt.Fprintf(os.Stderr, "  Key fingerprint: %s\n", fingerprint)

	// Format the known_hosts line
	// Format: hostname key-type base64-key
	keyType := key.Type()
	keyData := base64.StdEncoding.EncodeToString(key.Marshal())

	// Normalize hostname (include port if non-standard)
	normalizedHost := normalizeHostname(hostname, remote)

	line := fmt.Sprintf("%s %s %s\n", normalizedHost, keyType, keyData)

	// Append to known_hosts file
	f, err := os.OpenFile(knownHostsPath, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0600)
	if err != nil {
		return fmt.Errorf("failed to open known_hosts for writing: %w", err)
	}
	defer f.Close()

	if _, err := f.WriteString(line); err != nil {
		return fmt.Errorf("failed to write to known_hosts: %w", err)
	}

	return nil
}

func lockKnownHosts(knownHostsPath string) (*os.File, error) {
	f, err := os.OpenFile(knownHostsPath, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func unlockKnownHosts(f *os.File) {
	if f == nil {
		return
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	_ = f.Close()
}

// normalizeHostname formats hostname for known_hosts entry
func normalizeHostname(hostname string, remote net.Addr) string {
	// If hostname already has port, return as-is
	if strings.Contains(hostname, ":") {
		host, port, err := net.SplitHostPort(hostname)
		if err == nil && port != "22" {
			return fmt.Sprintf("[%s]:%s", host, port)
		}
		return host
	}

	// Extract port from remote address if non-standard
	if tcpAddr, ok := remote.(*net.TCPAddr); ok {
		if tcpAddr.Port != 22 {
			return fmt.Sprintf("[%s]:%d", hostname, tcpAddr.Port)
		}
	}

	return hostname
}

// fingerprintSHA256 returns the SHA256 fingerprint of a public key
func fingerprintSHA256(key ssh.PublicKey) string {
	hash := sha256.Sum256(key.Marshal())
	return "SHA256:" + base64.StdEncoding.EncodeToString(hash[:])
}

// RemoveHostKey removes a host from known_hosts (for key rotation)
func RemoveHostKey(knownHostsPath, hostname string) error {
	knownHostsMu.Lock()
	defer knownHostsMu.Unlock()

	lockFile, err := lockKnownHosts(knownHostsPath)
	if err != nil {
		return fmt.Errorf("failed to lock known_hosts: %w", err)
	}
	defer unlockKnownHosts(lockFile)

	// Read existing file
	data, err := os.ReadFile(knownHostsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // Nothing to remove
		}
		return err
	}

	// Filter out lines matching hostname
	var newLines []string
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := scanner.Text()
		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			newLines = append(newLines, line)
			continue
		}

		// Check if line starts with the hostname
		fields := strings.Fields(line)
		if len(fields) < 3 {
			newLines = append(newLines, line)
			continue
		}

		host := fields[0]
		// Check for exact match or bracketed match
		if host == hostname || host == fmt.Sprintf("[%s]", hostname) || strings.HasPrefix(host, hostname+":") || strings.HasPrefix(host, fmt.Sprintf("[%s]:", hostname)) {
			continue // Skip this line (remove it)
		}
		newLines = append(newLines, line)
	}

	// Write back
	return os.WriteFile(knownHostsPath, []byte(strings.Join(newLines, "\n")+"\n"), 0600)
}

// Run executes a command via SSH
func (c *Client) Run(ctx context.Context, command string) (*CommandResult, error) {
	result := &CommandResult{
		Command: command,
	}

	start := time.Now()
	defer func() {
		result.Duration = time.Since(start)
	}()

	// Create session
	session, err := c.conn.NewSession()
	if err != nil {
		result.Error = fmt.Errorf("failed to create session: %w", err)
		return result, result.Error
	}
	defer session.Close()

	// Capture stdout and stderr
	stdoutPipe, err := session.StdoutPipe()
	if err != nil {
		result.Error = fmt.Errorf("failed to create stdout pipe: %w", err)
		return result, result.Error
	}

	stderrPipe, err := session.StderrPipe()
	if err != nil {
		result.Error = fmt.Errorf("failed to create stderr pipe: %w", err)
		return result, result.Error
	}

	// Start command
	if err := session.Start(command); err != nil {
		result.Error = fmt.Errorf("failed to start command: %w", err)
		return result, result.Error
	}

	// Read output (with context cancellation)
	type output struct {
		stdout string
		stderr string
		err    error
	}
	outputChan := make(chan output, 1)

	go func() {
		stdoutBytes, _ := io.ReadAll(stdoutPipe)
		stderrBytes, _ := io.ReadAll(stderrPipe)

		err := session.Wait()

		outputChan <- output{
			stdout: string(stdoutBytes),
			stderr: string(stderrBytes),
			err:    err,
		}
	}()

	// Wait for completion or context cancellation
	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL) //nolint:errcheck // best-effort kill on cancellation
		result.Error = ctx.Err()
		return result, result.Error

	case out := <-outputChan:
		result.Stdout = strings.TrimSpace(out.stdout)
		result.Stderr = strings.TrimSpace(out.stderr)

		if out.err != nil {
			// Extract exit code from error
			var exitErr *ssh.ExitError
			if errors.As(out.err, &exitErr) {
				result.ExitCode = exitErr.ExitStatus()
			} else {
				result.ExitCode = -1
			}
			result.Error = out.err
		} else {
			result.ExitCode = 0
		}
	}

	return result, result.Error
}

// Ping validates the SSH connection is still alive.
func (c *Client) Ping(ctx context.Context) error {
	if c.pingFunc != nil {
		return c.pingFunc(ctx)
	}
	if c.conn == nil {
		return errors.New("ssh connection not initialized")
	}

	errCh := make(chan error, 1)
	go func() {
		_, _, err := c.conn.SendRequest("keepalive@openssh.com", true, nil)
		errCh <- err
	}()

	select {
	case <-ctx.Done():
		// If the underlying connection is wedged, SendRequest may block indefinitely.
		// Closing the connection forces the goroutine to unblock, avoiding leaks.
		_ = c.conn.Close()
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}

// RunScript uploads a script to a temp file and executes it
func (c *Client) RunScript(ctx context.Context, script string) (*CommandResult, error) {
	// Generate temp filename
	tempPath := fmt.Sprintf("/tmp/frameworks-script-%d.sh", time.Now().UnixNano())

	// Create local temp file
	localTemp := filepath.Join(os.TempDir(), filepath.Base(tempPath))
	if err := os.WriteFile(localTemp, []byte(script), 0700); err != nil {
		return nil, fmt.Errorf("failed to write script to temp file: %w", err)
	}
	defer os.Remove(localTemp)

	// Upload script
	if err := c.Upload(ctx, UploadOptions{
		LocalPath:  localTemp,
		RemotePath: tempPath,
		Mode:       0700,
	}); err != nil {
		return nil, fmt.Errorf("failed to upload script: %w", err)
	}

	// Execute script
	result, err := c.Run(ctx, tempPath)

	// Cleanup remote file (best-effort)
	_, _ = c.Run(ctx, fmt.Sprintf("rm -f %s", tempPath)) //nolint:errcheck // best-effort cleanup

	return result, err
}

// Upload transfers a file via SCP
func (c *Client) Upload(ctx context.Context, opts UploadOptions) error {
	// Read local file
	data, err := os.ReadFile(opts.LocalPath)
	if err != nil {
		return fmt.Errorf("failed to read local file: %w", err)
	}

	// Get file info for permissions
	info, err := os.Stat(opts.LocalPath)
	if err != nil {
		return fmt.Errorf("failed to stat local file: %w", err)
	}

	mode := opts.Mode
	if mode == 0 {
		mode = uint32(info.Mode().Perm())
	}

	// Create remote directory if needed
	remoteDir := filepath.Dir(opts.RemotePath)
	if _, errRun := c.Run(ctx, fmt.Sprintf("mkdir -p %s", ShellQuote(remoteDir))); errRun != nil {
		return fmt.Errorf("failed to create remote directory: %w", errRun)
	}

	// Create session for SCP
	session, err := c.conn.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	// Start SCP command
	stdinPipe, err := session.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdin pipe: %w", err)
	}

	if err := session.Start(fmt.Sprintf("scp -t %s", ShellQuote(opts.RemotePath))); err != nil {
		return fmt.Errorf("failed to start scp: %w", err)
	}

	// Send file header (mode + size + filename)
	filename := filepath.Base(opts.RemotePath)
	fmt.Fprintf(stdinPipe, "C%04o %d %s\n", mode, len(data), filename)

	// Send file data
	if _, err := stdinPipe.Write(data); err != nil {
		return fmt.Errorf("failed to write file data: %w", err)
	}

	// Send completion marker
	fmt.Fprint(stdinPipe, "\x00")
	stdinPipe.Close()

	// Wait for completion
	if err := session.Wait(); err != nil {
		return fmt.Errorf("scp failed: %w", err)
	}

	// Change ownership if specified
	if opts.Owner != "" {
		chownCmd := fmt.Sprintf("chown %s %s", ShellQuote(opts.Owner), ShellQuote(opts.RemotePath))
		if opts.Group != "" {
			chownCmd = fmt.Sprintf("chown %s:%s %s", ShellQuote(opts.Owner), ShellQuote(opts.Group), ShellQuote(opts.RemotePath))
		}
		if _, err := c.Run(ctx, chownCmd); err != nil {
			return fmt.Errorf("failed to change ownership: %w", err)
		}
	}

	return nil
}

// Close closes the SSH connection
func (c *Client) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}
