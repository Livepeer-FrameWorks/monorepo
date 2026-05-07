package grpc

import (
	"context"
	"database/sql"
	"net"

	"google.golang.org/grpc/peer"
)

// auditExecutor is the subset of *sql.Tx / *sql.DB writeSigningKeyAudit needs.
// Callers pass their own *sql.Tx so the audit row lands or rolls back with the
// underlying mutation — make it transactional so the audit is authoritative
// rather than best-effort telemetry.
type auditExecutor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// writeSigningKeyAudit records one row in commodore.signing_key_audit inside
// the caller's transaction. If the INSERT fails the caller's mutation rolls
// back: an authoritative audit means create/revoke either both happen and are
// logged or neither happens.
//
// No key material lives in this table — by design, only kid + actor + action.
// Used for create/revoke; per-use audit lives in signing_keys.last_used_at
// since per-USER_NEW row writes would scale poorly without a batched Kafka
// writer, which this codebase does not yet have.
func (s *CommodoreServer) writeSigningKeyAudit(
	ctx context.Context,
	exec auditExecutor,
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
	if _, err := exec.ExecContext(ctx, `
		INSERT INTO commodore.signing_key_audit
			(tenant_id, kid, action, actor_user_id, actor_ip, detail)
		VALUES ($1::uuid, $2, $3, $4, $5, $6)
	`, tenantID, kid, action, userIDArg, ipArg, detailArg); err != nil {
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
