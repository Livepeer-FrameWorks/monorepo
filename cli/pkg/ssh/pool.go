package ssh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Pool manages SSH connections to multiple hosts. It also carries the
// per-invocation default SSH key path (from the --ssh-key flag), which is
// injected into any ConnectionConfig that doesn't set one explicitly.
type Pool struct {
	connections    map[string]*Client
	mu             sync.RWMutex
	timeout        time.Duration
	defaultKeyPath string
	newClient      func(config *ConnectionConfig) (*Client, error)
	perHost        int
	hostSlots      map[string]chan struct{}
}

// NewPool creates a new connection pool. keyPath is the default SSH key
// applied when a ConnectionConfig does not set one; pass "" to rely solely
// on ssh-agent, ~/.ssh/config, and default identities.
func NewPool(timeout time.Duration, keyPath string) *Pool {
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	return &Pool{
		connections:    make(map[string]*Client),
		timeout:        timeout,
		defaultKeyPath: keyPath,
		newClient:      NewClient,
	}
}

// LimitPerHost bounds the ssh and scp processes that clients from this pool run against one address at a time, so
// callers that fan out checks stay under sshd's MaxStartups instead of having handshakes dropped. Zero is unbounded.
// It applies to clients created after the call.
func (p *Pool) LimitPerHost(n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.perHost = n
}

// DefaultKeyPath returns the pool's default SSH key path (empty if unset).
func (p *Pool) DefaultKeyPath() string {
	return p.defaultKeyPath
}

// cacheKey identifies a cached client by every field that affects transport
// behavior (target resolution, auth, host-key policy). Two calls that would
// produce different ssh argv must not share a cached client.
func cacheKey(c *ConnectionConfig) string {
	return fmt.Sprintf("%s|%s@%s:%d|key=%s|kh=%s|insecure=%t",
		c.HostName, c.User, c.Address, c.Port,
		c.KeyPath, c.KnownHostsPath, c.InsecureSkipVerify)
}

// Get retrieves or creates an SSH connection for a host. Caller's config is
// treated as read-only: pool defaults are applied to an internal copy, and
// the cache key is computed from that copy so subsequent Get/CloseHost calls
// with the same caller config always hit the same cache entry.
func (p *Pool) Get(config *ConnectionConfig) (*Client, error) {
	effective := p.effectiveConfig(config)
	key := cacheKey(&effective)
	if p.newClient == nil {
		p.newClient = NewClient
	}

	p.mu.RLock()
	if client, exists := p.connections[key]; exists {
		p.mu.RUnlock()
		return client, nil
	}
	p.mu.RUnlock()

	p.mu.Lock()
	defer p.mu.Unlock()

	if client, exists := p.connections[key]; exists {
		return client, nil
	}

	client, err := p.newClient(&effective)
	if err != nil {
		return nil, fmt.Errorf("failed to create SSH client for %s: %w", key, err)
	}

	if p.perHost > 0 {
		hostKey := fmt.Sprintf("%s:%d", effective.Address, effective.Port)
		if p.hostSlots == nil {
			p.hostSlots = map[string]chan struct{}{}
		}
		if p.hostSlots[hostKey] == nil {
			p.hostSlots[hostKey] = make(chan struct{}, p.perHost)
		}
		client.slots = p.hostSlots[hostKey]
	}
	p.connections[key] = client
	return client, nil
}

// effectiveConfig applies pool-level defaults (Timeout, KeyPath) to a copy of
// the caller's config, without mutating the original.
func (p *Pool) effectiveConfig(in *ConnectionConfig) ConnectionConfig {
	c := *in
	if c.Timeout == 0 {
		c.Timeout = p.timeout
	}
	if c.KeyPath == "" {
		c.KeyPath = p.defaultKeyPath
	}
	return c
}

func isConnectionError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, io.EOF) {
		return true
	}
	errText := err.Error()
	if strings.Contains(errText, "EOF") ||
		strings.Contains(errText, "connection reset by peer") ||
		strings.Contains(errText, "use of closed network connection") {
		return true
	}
	// OpenSSH exits 255 for its own failures and capitalizes the system error, e.g. "Connection reset by peer" or
	// "kex_exchange_identification: Connection closed by remote host" when sshd sheds concurrent handshakes.
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 255 {
		return false
	}
	lower := strings.ToLower(errText)
	return strings.Contains(lower, "connection reset by peer") || strings.Contains(lower, "connection closed by")
}

// Run executes a command on a host. Every ssh process opens its own connection, so there is no cached connection to
// probe first; a command whose connection drops is retried once on a freshly resolved client.
func (p *Pool) Run(ctx context.Context, config *ConnectionConfig, command string) (*CommandResult, error) {
	client, err := p.Get(config)
	if err != nil {
		return nil, err
	}

	result, err := client.Run(ctx, command)
	if err != nil && isConnectionError(err) {
		_ = p.CloseHost(config)
		client, retryErr := p.Get(config)
		if retryErr != nil {
			return result, retryErr
		}
		return client.Run(ctx, command)
	}

	return result, err
}

// Upload transfers a file to a host (creates/reuses connection)
func (p *Pool) Upload(ctx context.Context, config *ConnectionConfig, opts UploadOptions) error {
	client, err := p.Get(config)
	if err != nil {
		return err
	}

	if err := client.Upload(ctx, opts); err != nil {
		if !isConnectionError(err) {
			return err
		}

		_ = p.CloseHost(config)
		client, retryErr := p.Get(config)
		if retryErr != nil {
			return retryErr
		}
		return client.Upload(ctx, opts)
	}

	return nil
}

// Close closes all connections in the pool
func (p *Pool) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	var errs []error
	for key, client := range p.connections {
		if err := client.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close connection %s: %w", key, err))
		}
	}

	p.connections = make(map[string]*Client)
	return errors.Join(errs...)
}

// CloseHost closes the connection to a specific host
func (p *Pool) CloseHost(config *ConnectionConfig) error {
	effective := p.effectiveConfig(config)
	key := cacheKey(&effective)

	p.mu.Lock()
	defer p.mu.Unlock()

	if client, exists := p.connections[key]; exists {
		err := client.Close()
		delete(p.connections, key)
		return err
	}

	return nil
}

// Stats returns connection pool statistics
func (p *Pool) Stats() map[string]interface{} {
	p.mu.RLock()
	defer p.mu.RUnlock()

	return map[string]interface{}{
		"active_connections": len(p.connections),
		"timeout":            p.timeout,
	}
}
