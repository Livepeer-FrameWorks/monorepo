// Package identitytest installs identity resolvers for tests. Production
// wiring lives in cmd/foghorn/main.go; tests that exercise consumers of
// identity.Default() install a reduced facade over the layers they seed.
package identitytest

import (
	"testing"

	"frameworks/api_balancing/internal/identity"
	"frameworks/api_balancing/internal/state"
)

// InstallStateBacked wires an identity resolver over state.DefaultManager()
// only — no registry or Commodore legs — and uninstalls it on cleanup.
// Matches the single-Foghorn, in-memory-only deployment shape.
func InstallStateBacked(t *testing.T) {
	t.Helper()
	identity.SetDefault(identity.NewResolver(identity.Config{
		StreamState: func(internalName string) (identity.StreamStateView, bool) {
			ss := state.DefaultManager().GetStreamState(internalName)
			if ss == nil {
				return identity.StreamStateView{}, false
			}
			return identity.StreamStateView{
				StreamID:   ss.StreamID,
				PlaybackID: ss.PlaybackID,
				TenantID:   ss.TenantID,
				NodeID:     ss.NodeID,
			}, true
		},
		NodeCluster: func(nodeID string) string {
			if ns := state.DefaultManager().GetNodeState(nodeID); ns != nil {
				return ns.ClusterID
			}
			return ""
		},
	}))
	t.Cleanup(func() { identity.SetDefault(nil) })
}
