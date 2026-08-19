package tenants

import (
	"fmt"
	"os"
	"strings"

	"github.com/google/uuid"
)

var (
	ServiceAccountUserID = uuid.MustParse("00000000-0000-0000-0000-000000000000")
	SystemTenantID       = uuid.MustParse("00000000-0000-0000-0000-000000000001")
	AnonymousTenantID    = uuid.MustParse("00000000-0000-0000-0000-000000000002")
)

// RuntimeSystemTenantID returns the Quartermaster-owned system tenant identity
// supplied by deployment configuration. The reserved ID remains the local/demo
// default, but production may use the UUID persisted for the "frameworks"
// bootstrap alias.
func RuntimeSystemTenantID() (uuid.UUID, error) {
	raw := strings.TrimSpace(os.Getenv("SYSTEM_TENANT_ID"))
	if raw == "" {
		return SystemTenantID, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, fmt.Errorf("SYSTEM_TENANT_ID: %w", err)
	}
	return id, nil
}

// IsSystemTenant returns true for reserved tenant identifiers and for the
// deployment's Quartermaster-owned system tenant.
func IsSystemTenant(id uuid.UUID) bool {
	if id == ServiceAccountUserID || id == AnonymousTenantID {
		return true
	}
	runtimeID, err := RuntimeSystemTenantID()
	return err == nil && id == runtimeID
}
