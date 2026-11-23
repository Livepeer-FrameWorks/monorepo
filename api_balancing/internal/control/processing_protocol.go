package control

import "context"

// NodeProcessingProtocolOK uses authenticated registration metadata shared with
// the connection owner. Unknown ownership is checked by the final owning send.
func NodeProcessingProtocolOK(ctx context.Context, nodeID string) (supported, known bool) {
	if store := GetRedisStore(); store != nil {
		owner, protocol, err := store.GetConnOwnerProtocol(ctx, nodeID)
		if err != nil {
			return false, true
		}
		if owner.InstanceID != "" && protocol.Fence > 0 {
			return ControlFeaturesForProtocol(protocol.ProtocolVersion).ProcessingTenantEcho, true
		}
		return false, false
	}
	if session, ok := currentNodeSession(nodeID); ok {
		return ControlFeaturesForProtocol(session.ProtocolVersion).ProcessingTenantEcho, true
	}
	return false, false
}
