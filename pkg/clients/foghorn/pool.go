package foghorn

import (
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"google.golang.org/grpc/connectivity"
)

const defaultInternalServerName = "foghorn.internal"

// FoghornPool manages connections keyed by cluster_id and, within a cluster,
// by Foghorn address, with lazy creation, health checks, and idle eviction.
// A cell runs several Foghorns and callers round-robin across them, so a
// cluster holds one connection per instance; a call to a different instance
// must never replace (and thereby cancel every in-flight RPC on) the
// connection to another. Each connection gets its own auth and failsafe
// interceptors via NewGRPCClient.
type FoghornPool struct {
	mu      sync.RWMutex
	clients map[string]map[string]*poolEntry // cluster_id -> addr -> entry
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
	CACertFile          string
	ServerName          string
	AllowInsecure       bool
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
		clients: make(map[string]map[string]*poolEntry),
		config:  config,
		logger:  config.Logger,
		done:    make(chan struct{}),
	}
	go p.maintain()
	return p
}

// GetOrCreate returns the GRPCClient for the Foghorn at addr in clusterID,
// dialing it if this pool has no connection to that instance yet. Other
// instances' connections in the same cluster are left untouched: an instance
// that went away is dropped by the sweep, never by a call to its sibling.
func (p *FoghornPool) GetOrCreate(clusterID, addr string) (*GRPCClient, error) {
	// Fast path: read lock
	p.mu.RLock()
	if entry, ok := p.clients[clusterID][addr]; ok {
		entry.lastUsed.Store(time.Now().UnixNano())
		p.mu.RUnlock()
		return entry.client, nil
	}
	p.mu.RUnlock()

	// Slow path: write lock, double-check
	p.mu.Lock()
	defer p.mu.Unlock()

	if entry, ok := p.clients[clusterID][addr]; ok {
		entry.lastUsed.Store(time.Now().UnixNano())
		return entry.client, nil
	}

	client, err := NewGRPCClient(GRPCConfig{
		GRPCAddr:           addr,
		Timeout:            p.config.Timeout,
		Logger:             p.logger,
		ServiceToken:       p.config.ServiceToken,
		CircuitBreakerName: "foghorn-cell-" + clusterID,
		UseTLS:             p.config.UseTLS,
		CACertFile:         p.caCertFile(addr),
		ServerName:         p.serverName(addr),
		AllowInsecure:      p.config.AllowInsecure,
	})
	if err != nil {
		return nil, err
	}

	entry := &poolEntry{
		client: client,
		addr:   addr,
	}
	entry.lastUsed.Store(time.Now().UnixNano())
	if p.clients[clusterID] == nil {
		p.clients[clusterID] = make(map[string]*poolEntry)
	}
	p.clients[clusterID][addr] = entry

	p.logger.WithFields(logging.Fields{
		"cluster_id": clusterID,
		"addr":       addr,
	}).Info("Foghorn pool: created connection")

	return client, nil
}

func (p *FoghornPool) serverName(addr string) string {
	if p.config.AllowInsecure {
		return p.config.ServerName
	}
	if grpcutil.AddrIsFQDN(addr) && !isInternalFoghornAddr(addr) {
		host := addr
		if h, _, err := net.SplitHostPort(addr); err == nil {
			host = h
		}
		return host
	}
	if p.config.ServerName != "" {
		return p.config.ServerName
	}
	if p.config.UseTLS || p.config.CACertFile != "" {
		return defaultInternalServerName
	}
	return ""
}

func (p *FoghornPool) caCertFile(addr string) string {
	if p.config.AllowInsecure {
		return ""
	}
	if grpcutil.AddrIsFQDN(addr) && !isInternalFoghornAddr(addr) {
		return ""
	}
	return p.config.CACertFile
}

func isInternalFoghornAddr(addr string) bool {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	return host == defaultInternalServerName || strings.HasSuffix(host, ".internal") || host == "foghorn" || host == "localhost"
}

// Get returns a GRPCClient for clusterID if any of its instances is
// connected, preferring a Ready connection and, among those, the most
// recently used one.
func (p *FoghornPool) Get(clusterID string) (*GRPCClient, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var best *poolEntry
	bestReady := false
	for _, entry := range p.clients[clusterID] {
		ready := entry.client.conn.GetState() == connectivity.Ready
		if best == nil || (ready && !bestReady) || (ready == bestReady && entry.lastUsed.Load() > best.lastUsed.Load()) {
			best, bestReady = entry, ready
		}
	}
	if best == nil {
		return nil, false
	}
	best.lastUsed.Store(time.Now().UnixNano())
	return best.client, true
}

// Touch updates the last-used timestamp of every connection for clusterID,
// preventing idle eviction for connections with long-lived streams.
func (p *FoghornPool) Touch(clusterID string) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	now := time.Now().UnixNano()
	for _, entry := range p.clients[clusterID] {
		entry.lastUsed.Store(now)
	}
}

// Remove closes and removes every connection for clusterID.
func (p *FoghornPool) Remove(clusterID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	entries, ok := p.clients[clusterID]
	if !ok {
		return
	}
	for _, entry := range entries {
		_ = entry.client.Close()
	}
	delete(p.clients, clusterID)
	p.logger.WithFields(logging.Fields{"cluster_id": clusterID, "connections": len(entries)}).Info("Foghorn pool: removed connections")
}

// Close stops background maintenance and closes all connections.
func (p *FoghornPool) Close() error {
	close(p.done)
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, entries := range p.clients {
		for _, entry := range entries {
			_ = entry.client.Close()
		}
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
	for id, entries := range p.clients {
		for addr, entry := range entries {
			state := entry.client.conn.GetState()
			lastUsed := time.Unix(0, entry.lastUsed.Load())
			idle := now.Sub(lastUsed) > p.config.MaxIdleTime
			fields := logging.Fields{"cluster_id": id, "addr": addr}
			switch {
			case state == connectivity.Shutdown:
				p.logger.WithFields(fields).Info("Foghorn pool: removed shutdown connection")
			case idle && state == connectivity.TransientFailure:
				p.logger.WithFields(fields).Info("Foghorn pool: evicted idle+failing connection")
			case idle:
				fields["idle_for"] = now.Sub(lastUsed).String()
				p.logger.WithFields(fields).Info("Foghorn pool: evicted idle connection")
			default:
				continue
			}
			_ = entry.client.Close()
			delete(entries, addr)
		}
		if len(entries) == 0 {
			delete(p.clients, id)
		}
	}
}
