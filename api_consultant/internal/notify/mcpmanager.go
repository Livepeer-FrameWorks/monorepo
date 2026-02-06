package notify

import (
	"sync"

	"frameworks/pkg/logging"
	"frameworks/pkg/version"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TenantMCPManager maintains a dedicated MCP server per tenant.
// This guarantees tenant isolation: Sessions() on a tenant's server only
// returns that tenant's sessions.
type TenantMCPManager struct {
	mu      sync.RWMutex
	servers map[string]*mcp.Server
	logger  logging.Logger
}

func NewTenantMCPManager(logger logging.Logger) *TenantMCPManager {
	return &TenantMCPManager{
		servers: make(map[string]*mcp.Server),
		logger:  logger,
	}
}

// ServerForTenant returns the MCP server for the given tenant, creating one if
// it does not yet exist. Returns nil if tenantID is empty.
func (m *TenantMCPManager) ServerForTenant(tenantID string) *mcp.Server {
	if tenantID == "" {
		return nil
	}
	m.mu.RLock()
	s, ok := m.servers[tenantID]
	m.mu.RUnlock()
	if ok {
		return s
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok = m.servers[tenantID]
	if ok {
		return s
	}
	s = mcp.NewServer(&mcp.Implementation{
		Name:    "skipper-notify",
		Version: version.Version,
	}, nil)
	m.servers[tenantID] = s
	if m.logger != nil {
		m.logger.WithField("tenant_id", tenantID).Debug("Created MCP notification server for tenant")
	}
	return s
}

// SessionsForTenant returns all active sessions for the given tenant's server.
func (m *TenantMCPManager) SessionsForTenant(tenantID string) []*mcp.ServerSession {
	m.mu.RLock()
	s, ok := m.servers[tenantID]
	m.mu.RUnlock()
	if !ok {
		return nil
	}
	var sessions []*mcp.ServerSession
	for session := range s.Sessions() {
		sessions = append(sessions, session)
	}
	return sessions
}
