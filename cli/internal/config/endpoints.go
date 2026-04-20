package config

// ControlPlaneEndpoints returns the endpoint set callers should use for
// control-plane clients (services/mesh/dns/admin). Today it returns the
// context's endpoints verbatim; this helper exists so future env-level
// overrides (e.g. FRAMEWORKS_BRIDGE_URL) land in one place instead of
// being sprinkled through every command.
func ControlPlaneEndpoints(ctx Context, _ RuntimeOverrides) Endpoints {
	return ctx.Endpoints
}
