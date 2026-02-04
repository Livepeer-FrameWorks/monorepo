package tenants

import "github.com/google/uuid"

var (
	ServiceAccountUserID = uuid.MustParse("00000000-0000-0000-0000-000000000000")
	SystemTenantID       = uuid.MustParse("00000000-0000-0000-0000-000000000001")
	AnonymousTenantID    = uuid.MustParse("00000000-0000-0000-0000-000000000002")
)

// IsSystemTenant returns true for reserved tenant identifiers.
func IsSystemTenant(id uuid.UUID) bool {
	return id == ServiceAccountUserID || id == SystemTenantID || id == AnonymousTenantID
}
