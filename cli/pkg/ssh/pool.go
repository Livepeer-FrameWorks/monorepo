package ssh

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Pool manages SSH connections to multiple hosts
type Pool struct {
	connections map[string]*Client
	mu          sync.RWMutex
	timeout     time.Duration
}

// NewPool creates a new connection pool
func NewPool(timeout time.Duration) *Pool {
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	return &Pool{
		connections: make(map[string]*Client),
		timeout:     timeout,
	}
}

// Get retrieves or creates an SSH connection for a host
func (p *Pool) Get(config *ConnectionConfig) (*Client, error) {
	key := fmt.Sprintf("%s@%s:%d", config.User, config.Address, config.Port)

	// Try to get existing connection (read lock)
	p.mu.RLock()
	if client, exists := p.connections[key]; exists {
		p.mu.RUnlock()
		return client, nil
	}
	p.mu.RUnlock()

	// Create new connection (write lock)
	p.mu.Lock()
	defer p.mu.Unlock()

	// Double-check after acquiring write lock (another goroutine might have created it)
	if client, exists := p.connections[key]; exists {
		return client, nil
	}

	// Set timeout if not configured
	if config.Timeout == 0 {
		config.Timeout = p.timeout
	}

	// Create new client
	client, err := NewClient(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create SSH client for %s: %w", key, err)
	}

	p.connections[key] = client
	return client, nil
}

// Run executes a command on a host (creates/reuses connection)
func (p *Pool) Run(ctx context.Context, config *ConnectionConfig, command string) (*CommandResult, error) {
	client, err := p.Get(config)
	if err != nil {
		return nil, err
	}

	return client.Run(ctx, command)
}

// Upload transfers a file to a host (creates/reuses connection)
func (p *Pool) Upload(ctx context.Context, config *ConnectionConfig, opts UploadOptions) error {
	client, err := p.Get(config)
	if err != nil {
		return err
	}

	return client.Upload(ctx, opts)
}

// Close closes all connections in the pool
func (p *Pool) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	var lastErr error
	for key, client := range p.connections {
		if err := client.Close(); err != nil {
			lastErr = fmt.Errorf("failed to close connection %s: %w", key, err)
		}
	}

	p.connections = make(map[string]*Client)
	return lastErr
}

// CloseHost closes the connection to a specific host
func (p *Pool) CloseHost(config *ConnectionConfig) error {
	key := fmt.Sprintf("%s@%s:%d", config.User, config.Address, config.Port)

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
