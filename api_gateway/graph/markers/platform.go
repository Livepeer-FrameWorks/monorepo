package markers

// Platform god-view marker types. Platform is resolver-driven (no fields);
// the detail markers carry the target tenant so the per-tab sub-resolvers
// (billing, content, analytics) can resolve lazily without re-fetching.
type Platform struct{}

type TenantAdminDetail struct {
	TenantID string
}

type TenantAdminBilling struct {
	TenantID string
}
