package grpc

import (
	"context"
	"database/sql"
	"net"

	"frameworks/api_control/internal/database/commodoredb"
	"google.golang.org/grpc/peer"
)

// writeSigningKeyAudit records one row in commodore.signing_key_audit inside
// the caller's transaction. If the INSERT fails the caller's mutation rolls
// back: an authoritative audit means create/revoke either both happen and are
// logged or neither happens.
//
// No key material lives in this table — by design, only kid + actor + action.
// Used for create/revoke; per-use audit is represented by
// signing_keys.last_used_at to avoid one database write per playback
// authorization request.
func (s *CommodoreServer) writeSigningKeyAudit(
	ctx context.Context,
	exec commodoredb.DBTX,
	tenantID, kid, action, actorUserID, detail string,
) error {
	if tenantID == "" || kid == "" || action == "" {
		return nil
	}
	actorIP := peerIPFromContext(ctx)
	var (
		userIDArg sql.NullString
		ipArg     sql.NullString
		detailArg sql.NullString
	)
	if actorUserID != "" {
		userIDArg = sql.NullString{String: actorUserID, Valid: true}
	}
	if actorIP != "" {
		ipArg = sql.NullString{String: actorIP, Valid: true}
	}
	if detail != "" {
		detailArg = sql.NullString{String: detail, Valid: true}
	}
	if err := commodoredb.New(exec).InsertSigningKeyAudit(ctx, commodoredb.InsertSigningKeyAuditParams{
		TenantID: tenantID, ActorUserID: userIDArg, Kid: kid, Action: action, ActorIp: ipArg, Detail: detailArg,
	}); err != nil {
		s.logger.WithError(err).Warn("signing-key audit write failed")
		return err
	}
	return nil
}

// peerIPFromContext returns the remote IP for the gRPC peer, or "" when the
// transport has no addressable peer (e.g. unit tests). Strips the port; the
// audit log holds the raw IP so dashboards can group by origin.
func peerIPFromContext(ctx context.Context) string {
	pr, ok := peer.FromContext(ctx)
	if !ok || pr.Addr == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(pr.Addr.String())
	if err != nil {
		return pr.Addr.String()
	}
	return host
}
