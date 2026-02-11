package foghorn

import (
	"sync"
	"sync/atomic"
	"time"

	"frameworks/pkg/logging"

	"google.golang.org/grpc/connectivity"
)

// FoghornPool manages a map of cluster_id -> *GRPCClient with lazy creation,
// health checks, and idle eviction. Each connection gets its own auth and
// failsafe interceptors via NewGRPCClient.
type FoghornPool struct {
	mu      sync.RWMutex
	clients map[string]*poolEntry
	config  PoolConfig
	logger  logging.Logger
	done    chan struct{}
}

type poolEntry struct {
	client   *GRPCClient
	addr     string
	lastUsed atomic.Int64 // UnixNano timestamp; safe for concurrent access under RLock
}

// PoolConfig configures the Foghorn connection pool.
type PoolConfig struct {
	ServiceToken        string
	Timeout             time.Duration // per-call gRPC timeout (default 30s)
	Logger              logging.Logger
	MaxIdleTime         time.Duration // evict connections idle longer than this (default 10m)
	HealthCheckInterval time.Duration // background sweep interval (default 30s)
	UseTLS              bool          // enable TLS transport for all pooled connections
}

func (c PoolConfig) withDefaults() PoolConfig {
	if c.Timeout == 0 {
		c.Timeout = 30 * time.Second
	}
	if c.MaxIdleTime == 0 {
		c.MaxIdleTime = 10 * time.Minute
	}
	if c.HealthCheckInterval == 0 {
		c.HealthCheckInterval = 30 * time.Second
	}
	return c
}

// NewPool creates a FoghornPool and starts background maintenance.
func NewPool(config PoolConfig) *FoghornPool {
	config = config.withDefaults()
	p := &FoghornPool{
		clients: make(map[string]*poolEntry),
		config:  config,
		logger:  config.Logger,
		done:    make(chan struct{}),
	}
	go p.maintain()
	return p
}

// GetOrCreate returns the GRPCClient for clusterID, creating a new connection
// to addr if one doesn't exist. If the entry exists but addr differs (Foghorn
// moved), the old connection is replaced.
func (p *FoghornPool) GetOrCreate(clusterID, addr string) (*GRPCClient, error) {
	// Fast path: read lock
	p.mu.RLock()
	if entry, ok := p.clients[clusterID]; ok && entry.addr == addr {
		entry.lastUsed.Store(time.Now().UnixNano())
		p.mu.RUnlock()
		return entry.client, nil
	}
	p.mu.RUnlock()

	// Slow path: write lock, double-check
	p.mu.Lock()
	defer p.mu.Unlock()

	if entry, ok := p.clients[clusterID]; ok {
		if entry.addr == addr {
			entry.lastUsed.Store(time.Now().UnixNano())
			return entry.client, nil
		}
		// Address changed — close old connection
		_ = entry.client.Close()
		delete(p.clients, clusterID)
	}

	client, err := NewGRPCClient(GRPCConfig{
		GRPCAddr:     addr,
		Timeout:      p.config.Timeout,
		Logger:       p.logger,
		ServiceToken: p.config.ServiceToken,
		UseTLS:       p.config.UseTLS,
	})
	if err != nil {
		return nil, err
	}

	entry := &poolEntry{
		client: client,
		addr:   addr,
	}
	entry.lastUsed.Store(time.Now().UnixNano())
	p.clients[clusterID] = entry

	p.logger.WithFields(logging.Fields{
		"cluster_id": clusterID,
		"addr":       addr,
	}).Info("Foghorn pool: created connection")

	return client, nil
}

// Get returns the GRPCClient for clusterID if it exists.
func (p *FoghornPool) Get(clusterID string) (*GRPCClient, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	entry, ok := p.clients[clusterID]
	if ok {
		entry.lastUsed.Store(time.Now().UnixNano())
		return entry.client, true
	}
	return nil, false
}

// Touch updates the last-used timestamp for clusterID, preventing idle eviction
// for connections with long-lived streams.
func (p *FoghornPool) Touch(clusterID string) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if entry, ok := p.clients[clusterID]; ok {
		entry.lastUsed.Store(time.Now().UnixNano())
	}
}

// Remove closes and removes the connection for clusterID.
func (p *FoghornPool) Remove(clusterID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if entry, ok := p.clients[clusterID]; ok {
		_ = entry.client.Close()
		delete(p.clients, clusterID)
		p.logger.WithField("cluster_id", clusterID).Info("Foghorn pool: removed connection")
	}
}

// Close stops background maintenance and closes all connections.
func (p *FoghornPool) Close() error {
	close(p.done)
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, entry := range p.clients {
		_ = entry.client.Close()
		delete(p.clients, id)
	}
	return nil
}

// maintain runs periodic health checks and idle eviction.
func (p *FoghornPool) maintain() {
	ticker := time.NewTicker(p.config.HealthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-p.done:
			return
		case <-ticker.C:
			p.sweep()
		}
	}
}

// sweep removes unhealthy and idle connections.
func (p *FoghornPool) sweep() {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	for id, entry := range p.clients {
		state := entry.client.conn.GetState()
		lastUsed := time.Unix(0, entry.lastUsed.Load())
		idle := now.Sub(lastUsed) > p.config.MaxIdleTime

		if state == connectivity.Shutdown {
			_ = entry.client.Close()
			delete(p.clients, id)
			p.logger.WithField("cluster_id", id).Info("Foghorn pool: removed shutdown connection")
			continue
		}

		if idle && state == connectivity.TransientFailure {
			_ = entry.client.Close()
			delete(p.clients, id)
			p.logger.WithField("cluster_id", id).Info("Foghorn pool: evicted idle+failing connection")
			continue
		}

		if idle {
			_ = entry.client.Close()
			delete(p.clients, id)
			p.logger.WithFields(logging.Fields{
				"cluster_id": id,
				"idle_for":   now.Sub(lastUsed).String(),
			}).Info("Foghorn pool: evicted idle connection")
		}
	}
}
