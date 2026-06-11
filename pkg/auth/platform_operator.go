package auth

import (
	"strings"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/tenants"
)

// IsPlatformOperator is the staff-flag check for platform-wide admin
// surfaces (/admin): the caller must be an owner/admin of the
// reserved system tenant. It deliberately mirrors the break-glass arm of
// CanAdminMistNode so "platform staff" means the same thing everywhere.
func IsPlatformOperator(callerTenantID, callerRole string) bool {
	callerTenantID = strings.TrimSpace(callerTenantID)
	callerRole = strings.ToLower(strings.TrimSpace(callerRole))
	if callerTenantID != tenants.SystemTenantID.String() {
		return false
	}
	return mistAdminPrivilegedRole(callerRole)
}
