package sessionrevocations

import (
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	internalv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	"google.golang.org/protobuf/proto"
)

// DecodePayload converts a verified typed revocation into the local projection.
// Transport adapters must establish source, visibility and tenant scope first.
func DecodePayload(tenant, kind string, payload proto.Message) *commodorepb.SessionRevocation {
	if tenant == "" {
		return nil
	}
	rev := &commodorepb.SessionRevocation{TenantId: tenant}
	switch v := payload.(type) {
	case *internalv1.AuthSessionRevoked:
		if kind != "auth_session.revoked" || v.GetSessionId() == "" {
			return nil
		}
		rev.UserId, rev.SessionId, rev.RevokedAt = v.GetUserId(), v.GetSessionId(), v.GetRevokedAt()
	case *internalv1.UserSessionsRevoked:
		if kind != "user.sessions_revoked" {
			return nil
		}
		rev.UserId, rev.RevokedAt = v.GetUserId(), v.GetRevokedAt()
		rev.AccountDeactivated = v.GetReason() == internalv1.SessionRevocationReason_SESSION_REVOCATION_REASON_OPERATOR_DEACTIVATION
	default:
		return nil
	}
	if rev.UserId == "" || !rev.GetRevokedAt().IsValid() {
		return nil
	}
	return rev
}
