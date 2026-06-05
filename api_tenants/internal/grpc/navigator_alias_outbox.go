package grpc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/outbox"
	dnspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/dns"
)

const (
	aliasOutboxBaseBackoff        = 2 * time.Second
	aliasOutboxMaxBackoff         = 1 * time.Hour
	aliasOutboxBatchSize          = 16
	aliasOutboxPollPeriod         = 30 * time.Second
	aliasOutboxLease              = 60 * time.Second
	aliasOutboxAlertAfterAttempts = 12
)

func aliasOutboxConfig() outbox.Config {
	return outbox.Config{
		BaseBackoff:        aliasOutboxBaseBackoff,
		MaxBackoff:         aliasOutboxMaxBackoff,
		BatchSize:          aliasOutboxBatchSize,
		PollPeriod:         aliasOutboxPollPeriod,
		Lease:              aliasOutboxLease,
		AlertAfterAttempts: aliasOutboxAlertAfterAttempts,
	}
}

type aliasOutboxRow struct {
	id        string
	tenantID  string
	subdomain string
	clusterID string
	reason    string
	action    string
	attempts  int
}

// EnqueueNavigatorTenantAliasTx persists one desired Navigator alias action
// inside the caller's tx. Each row is self-contained — the paid/active
// decision is made at enqueue time, so the drain worker dispatches purely
// from stored fields. action must be one of ensure, retire, remove,
// remove_cluster.
//
// When a transition needs more than one action (e.g. retire(old) +
// ensure(new) on a rename), call this once per row in intended order: the
// BIGSERIAL seq reflects that order and the worker serializes per tenant by
// seq, never created_at (same-tx rows share a timestamp).
func (s *QuartermasterServer) EnqueueNavigatorTenantAliasTx(
	ctx context.Context,
	exec interface {
		QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	},
	tenantID, subdomain, action, clusterID, reason string,
) (string, error) {
	if tenantID == "" {
		return "", errors.New("tenant_id required")
	}
	switch action {
	case "ensure", "retire":
		if subdomain == "" {
			return "", fmt.Errorf("action %q requires a subdomain", action)
		}
	case "remove":
		// tenant-only; subdomain optional (audit)
	case "remove_cluster":
		if clusterID == "" {
			return "", errors.New("action remove_cluster requires a cluster_id")
		}
	default:
		return "", fmt.Errorf("unsupported action %q", action)
	}
	var id string
	row := exec.QueryRowContext(ctx, `
		INSERT INTO quartermaster.navigator_tenant_alias_outbox
			(tenant_id, subdomain, cluster_id, reason, action)
		VALUES ($1::uuid, NULLIF($2, ''), NULLIF($3, ''), NULLIF($4, ''), $5)
		RETURNING id
	`, tenantID, subdomain, clusterID, reason, action)
	if err := row.Scan(&id); err != nil {
		return "", fmt.Errorf("insert navigator_tenant_alias_outbox: %w", err)
	}
	return id, nil
}

type aliasOutboxStore struct {
	server *QuartermasterServer
}

func (st *aliasOutboxStore) ClaimBatch(ctx context.Context, _ int, _ time.Duration) ([]outbox.Claim[aliasOutboxRow], error) {
	rows, err := st.server.claimAliasOutboxBatch(ctx)
	if err != nil {
		return nil, err
	}
	claims := make([]outbox.Claim[aliasOutboxRow], 0, len(rows))
	for _, r := range rows {
		claims = append(claims, outbox.Claim[aliasOutboxRow]{
			ID:       r.id,
			Attempts: r.attempts,
			Payload:  r,
		})
	}
	return claims, nil
}

func (st *aliasOutboxStore) MarkCompleted(ctx context.Context, id string) error {
	return st.server.markAliasOutboxCompleted(ctx, id)
}

func (st *aliasOutboxStore) RecordFailure(ctx context.Context, id string, attempts int, _ []string, cause error, backoff time.Duration) error {
	return st.server.recordAliasOutboxFailure(ctx, id, attempts, cause, backoff)
}

type aliasOutboxDispatcher struct {
	server *QuartermasterServer
}

func (d *aliasOutboxDispatcher) Dispatch(ctx context.Context, row aliasOutboxRow) ([]string, error) {
	return d.server.dispatchAliasOutboxRow(ctx, row)
}

// runNavigatorTenantAliasOutboxWorker drains pending subdomain-alias rows
// until Navigator confirms each hand-off. Safe on every QM replica — the
// per-tenant claim predicate keeps at most one row per tenant in flight, so
// a newer remove never overtakes an older ensure.
func (s *QuartermasterServer) runNavigatorTenantAliasOutboxWorker(ctx context.Context) {
	if s.navigatorClient == nil {
		s.logger.Info("navigator tenant-alias outbox worker disabled: no navigator client")
		return
	}
	cfg := aliasOutboxConfig()
	// recordAliasOutboxFailure emits its own (tenant-queue-blocked) alert, so
	// suppress the generic worker's duplicate.
	cfg.AlertAfterAttempts = 0
	worker := &outbox.Worker[aliasOutboxRow]{
		Config:     cfg,
		Store:      &aliasOutboxStore{server: s},
		Dispatcher: &aliasOutboxDispatcher{server: s},
		Logger:     s.logger,
		AlertLabel: "navigator tenant alias",
	}
	worker.Run(ctx)
}

// claimAliasOutboxBatch claims at most one in-flight row per tenant: the
// lowest-seq incomplete row, skipping any tenant that already has an older
// incomplete row in flight. This serializes dispatch per tenant across
// replicas (seq order, not created_at) while staying parallel across tenants.
func (s *QuartermasterServer) claimAliasOutboxBatch(ctx context.Context) ([]aliasOutboxRow, error) {
	var out []aliasOutboxRow
	err := database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		rows, qerr := tx.QueryContext(ctx, `
			SELECT o.id::text, o.tenant_id::text,
			       COALESCE(o.subdomain, ''), COALESCE(o.cluster_id, ''),
			       COALESCE(o.reason, ''), o.action, o.attempts
			FROM quartermaster.navigator_tenant_alias_outbox o
			WHERE o.completed_at IS NULL
				  AND (o.claimed_at IS NULL OR o.claimed_at < NOW() - $1::interval)
				  AND (o.next_retry_at IS NULL OR o.next_retry_at <= NOW())
				  AND NOT EXISTS (
			      SELECT 1
			      FROM quartermaster.navigator_tenant_alias_outbox o2
			      WHERE o2.tenant_id = o.tenant_id
			        AND o2.completed_at IS NULL
			        AND o2.seq < o.seq
			  )
			ORDER BY o.seq
			FOR UPDATE SKIP LOCKED
			LIMIT $2
		`, fmt.Sprintf("%d seconds", int(aliasOutboxLease.Seconds())), aliasOutboxBatchSize)
		if qerr != nil {
			return qerr
		}
		defer rows.Close()

		batch := make([]aliasOutboxRow, 0, aliasOutboxBatchSize)
		for rows.Next() {
			var r aliasOutboxRow
			if scanErr := rows.Scan(&r.id, &r.tenantID, &r.subdomain, &r.clusterID, &r.reason, &r.action, &r.attempts); scanErr != nil {
				return scanErr
			}
			batch = append(batch, r)
		}
		if rowsErr := rows.Err(); rowsErr != nil {
			return rowsErr
		}
		if len(batch) > 0 {
			ids := make([]string, 0, len(batch))
			for _, r := range batch {
				ids = append(ids, r.id)
			}
			if _, uerr := tx.ExecContext(ctx, `
				UPDATE quartermaster.navigator_tenant_alias_outbox
				SET claimed_at = NOW()
				WHERE id = ANY($1::uuid[])
			`, qmOutboxIDArray(ids)); uerr != nil {
				return uerr
			}
		}
		out = batch
		return nil
	})
	return out, err
}

func (s *QuartermasterServer) markAliasOutboxCompleted(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, `
		UPDATE quartermaster.navigator_tenant_alias_outbox
		SET completed_at = NOW(), last_error = NULL, next_retry_at = NULL
		WHERE id = $1::uuid
	`, id); err != nil {
		return fmt.Errorf("mark navigator tenant-alias outbox row completed: %w", err)
	}
	return nil
}

// recordAliasOutboxFailure persists the failure and clears the claim so the row
// retries after its backoff. attempts is the count before this failure; the row
// increments in-place so the counter advances and the alert threshold fires, and
// next_retry_at gates the next claim. Failing rows are never auto-completed —
// that would silently drop the intent (e.g. an ensure that never lands); a poison
// row deliberately blocks its tenant's queue, and the alert surfaces it.
func (s *QuartermasterServer) recordAliasOutboxFailure(ctx context.Context, id string, attempts int, cause error, backoff time.Duration) error {
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	if backoff <= 0 {
		backoff = aliasOutboxBaseBackoff
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE quartermaster.navigator_tenant_alias_outbox
		SET attempts = attempts + 1,
		    last_error = $2,
		    claimed_at = NULL,
			next_retry_at = NOW() + $3::interval
		WHERE id = $1::uuid
	`, id, msg, fmt.Sprintf("%d milliseconds", backoff.Milliseconds())); err != nil {
		return fmt.Errorf("record navigator tenant-alias outbox failure: %w", err)
	}
	if newAttempts := attempts + 1; newAttempts >= aliasOutboxAlertAfterAttempts {
		s.logger.WithFields(logging.Fields{
			"outbox_id": id,
			"attempts":  newAttempts,
			"cause":     msg,
		}).Error("Navigator tenant-alias hand-off failing repeatedly — Navigator reachability degraded; tenant queue blocked until it lands")
	}
	return nil
}

func (s *QuartermasterServer) dispatchAliasOutboxRow(ctx context.Context, row aliasOutboxRow) ([]string, error) {
	if s.navigatorClient == nil {
		return []string{"navigator"}, errors.New("navigator client not configured")
	}
	switch row.action {
	case "ensure":
		resp, err := s.navigatorClient.EnsureTenantAlias(ctx, &dnspb.EnsureTenantAliasRequest{
			TenantId:  row.tenantID,
			Subdomain: row.subdomain,
		})
		if err != nil {
			return []string{"navigator"}, err
		}
		if resp == nil {
			return []string{"navigator"}, errors.New("navigator ensure tenant alias returned nil response")
		}
		if !resp.GetAccepted() || resp.GetError() != "" {
			return []string{"navigator"}, fmt.Errorf("navigator ensure tenant alias rejected: %s", resp.GetError())
		}
	case "retire":
		resp, err := s.navigatorClient.RemoveTenantAliasSubdomain(ctx, &dnspb.RemoveTenantAliasSubdomainRequest{
			TenantId:  row.tenantID,
			Subdomain: row.subdomain,
		})
		if err != nil {
			return []string{"navigator"}, err
		}
		if resp == nil {
			return []string{"navigator"}, errors.New("navigator retire tenant alias returned nil response")
		}
		if !resp.GetAccepted() || resp.GetError() != "" {
			return []string{"navigator"}, fmt.Errorf("navigator retire tenant alias rejected: %s", resp.GetError())
		}
	case "remove":
		resp, err := s.navigatorClient.RemoveTenantAlias(ctx, &dnspb.RemoveTenantAliasRequest{
			TenantId: row.tenantID,
		})
		if err != nil {
			return []string{"navigator"}, err
		}
		if resp == nil {
			return []string{"navigator"}, errors.New("navigator remove tenant alias returned nil response")
		}
		if !resp.GetAccepted() {
			return []string{"navigator"}, errors.New("navigator remove tenant alias rejected")
		}
	case "remove_cluster":
		resp, err := s.navigatorClient.RemoveTenantAliasCluster(ctx, &dnspb.RemoveTenantAliasClusterRequest{
			TenantId:  row.tenantID,
			ClusterId: row.clusterID,
		})
		if err != nil {
			return []string{"navigator"}, err
		}
		if resp == nil {
			return []string{"navigator"}, errors.New("navigator remove tenant alias cluster returned nil response")
		}
		if !resp.GetAccepted() {
			return []string{"navigator"}, errors.New("navigator remove tenant alias cluster rejected")
		}
	default:
		return []string{"navigator"}, fmt.Errorf("unsupported action %q", row.action)
	}
	return nil, nil
}
