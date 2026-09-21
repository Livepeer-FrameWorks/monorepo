package tenants

import (
	"github.com/google/uuid"
)

// Reserved platform identities. SystemTenantID is the local and demo system
// tenant; a deployment may map its system tenant to another UUID through
// config.SystemTenant.
var (
	ServiceAccountUserID = uuid.MustParse("00000000-0000-0000-0000-000000000000")
	SystemTenantID       = uuid.MustParse("00000000-0000-0000-0000-000000000001")
	AnonymousTenantID    = uuid.MustParse("00000000-0000-0000-0000-000000000002")
)
