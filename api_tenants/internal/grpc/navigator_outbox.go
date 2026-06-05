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
	navOutboxBaseBackoff        = 2 * time.Second
	navOutboxMaxBackoff         = 1 * time.Hour
	navOutboxBatchSize          = 16
	navOutboxPollPeriod         = 30 * time.Second
	navOutboxLease              = 60 * time.Second
	navOutboxAlertAfterAttempts = 12
)

func navOutboxConfig() outbox.Config {
	return outbox.Config{
		BaseBackoff:        navOutboxBaseBackoff,
		MaxBackoff:         navOutboxMaxBackoff,
		BatchSize:          navOutboxBatchSize,
		PollPeriod:         navOutboxPollPeriod,
		Lease:              navOutboxLease,
		AlertAfterAttempts: navOutboxAlertAfterAttempts,
	}
}

type navOutboxRow struct {
	id        string
	tenantID  string
	domain    string
	action    string
	attempts  int
	createdAt time.Time
}

// EnqueueNavigatorCustomDomainTx persists the desired Navigator action for a
// (tenant_id, domain, action) tuple inside the caller's tx. Failed Navigator
// dispatch retries via exponential backoff; the row is durable until the
// hand-off lands. action must be "ensure" or "remove".
func (s *QuartermasterServer) EnqueueNavigatorCustomDomainTx(
	ctx context.Context,
	exec interface {
		QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	},
	tenantID, domain, action string,
) (string, error) {
	if tenantID == "" || domain == "" {
		return "", errors.New("tenant_id and domain required")
	}
	if action != "ensure" && action != "remove" {
		return "", fmt.Errorf("unsupported action %q", action)
	}
	var id string
	row := exec.QueryRowContext(ctx, `
		INSERT INTO quartermaster.navigator_custom_domain_outbox
			(tenant_id, domain, action)
		VALUES ($1::uuid, $2, $3)
		RETURNING id
	`, tenantID, domain, action)
	if err := row.Scan(&id); err != nil {
		return "", fmt.Errorf("insert navigator_custom_domain_outbox: %w", err)
	}
	return id, nil
}

type navOutboxStore struct {
	server *QuartermasterServer
}

func (st *navOutboxStore) ClaimBatch(ctx context.Context, _ int, _ time.Duration) ([]outbox.Claim[navOutboxRow], error) {
	rows, err := st.server.claimNavOutboxBatch(ctx)
	if err != nil {
		return nil, err
	}
	claims := make([]outbox.Claim[navOutboxRow], 0, len(rows))
	for _, r := range rows {
		claims = append(claims, outbox.Claim[navOutboxRow]{
			ID:       r.id,
			Attempts: r.attempts,
			Payload:  r,
		})
	}
	return claims, nil
}

func (st *navOutboxStore) MarkCompleted(ctx context.Context, id string) error {
	st.server.markNavOutboxCompleted(ctx, id)
	return nil
}

func (st *navOutboxStore) RecordFailure(ctx context.Context, id string, attempts int, _ []string, cause error, _ time.Duration) error {
	st.server.recordNavOutboxFailure(ctx, id, attempts, cause)
	return nil
}

type navOutboxDispatcher struct {
	server *QuartermasterServer
}

func (d *navOutboxDispatcher) Dispatch(ctx context.Context, row navOutboxRow) ([]string, error) {
	return d.server.dispatchNavOutboxRow(ctx, row)
}

// runNavigatorCustomDomainOutboxWorker drains pending rows until Navigator
// confirms the hand-off. Safe on every QM replica — SKIP LOCKED + lease
// makes work distributable.
func (s *QuartermasterServer) runNavigatorCustomDomainOutboxWorker(ctx context.Context) {
	if s.navigatorClient == nil {
		s.logger.Info("navigator custom-domain outbox worker disabled: no navigator client")
		return
	}
	cfg := navOutboxConfig()
	cfg.AlertAfterAttempts = 0
	worker := &outbox.Worker[navOutboxRow]{
		Config:     cfg,
		Store:      &navOutboxStore{server: s},
		Dispatcher: &navOutboxDispatcher{server: s},
		Logger:     s.logger,
		AlertLabel: "navigator custom domain",
	}
	worker.Run(ctx)
}

func (s *QuartermasterServer) claimNavOutboxBatch(ctx context.Context) ([]navOutboxRow, error) {
	var out []navOutboxRow
	err := database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		rows, qerr := tx.QueryContext(ctx, `
			SELECT id::text, tenant_id::text, domain, action, attempts, created_at
			FROM quartermaster.navigator_custom_domain_outbox
			WHERE completed_at IS NULL
			  AND (claimed_at IS NULL OR claimed_at < NOW() - $1::interval)
			ORDER BY created_at
			FOR UPDATE SKIP LOCKED
			LIMIT $2
		`, fmt.Sprintf("%d seconds", int(navOutboxLease.Seconds())), navOutboxBatchSize)
		if qerr != nil {
			return qerr
		}
		defer rows.Close()

		batch := make([]navOutboxRow, 0, navOutboxBatchSize)
		for rows.Next() {
			var r navOutboxRow
			if scanErr := rows.Scan(&r.id, &r.tenantID, &r.domain, &r.action, &r.attempts, &r.createdAt); scanErr != nil {
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
				UPDATE quartermaster.navigator_custom_domain_outbox
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

func (s *QuartermasterServer) markNavOutboxCompleted(ctx context.Context, id string) {
	if _, err := s.db.ExecContext(ctx, `
		UPDATE quartermaster.navigator_custom_domain_outbox
		SET completed_at = NOW(), last_error = NULL
		WHERE id = $1::uuid
	`, id); err != nil {
		s.logger.WithError(err).WithField("outbox_id", id).
			Warn("Failed to mark navigator custom-domain outbox row completed")
	}
}

func (s *QuartermasterServer) recordNavOutboxFailure(ctx context.Context, id string, attempts int, cause error) {
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE quartermaster.navigator_custom_domain_outbox
		SET attempts = $2, last_error = $3, claimed_at = NULL
		WHERE id = $1::uuid
	`, id, attempts, msg); err != nil {
		s.logger.WithError(err).WithField("outbox_id", id).
			Warn("Failed to record navigator custom-domain outbox failure")
	}
	if attempts >= navOutboxAlertAfterAttempts {
		s.logger.WithFields(logging.Fields{
			"outbox_id": id,
			"attempts":  attempts,
			"cause":     msg,
		}).Error("Navigator custom-domain hand-off failing repeatedly — Navigator reachability degraded")
	}
}

func (s *QuartermasterServer) dispatchNavOutboxRow(ctx context.Context, row navOutboxRow) ([]string, error) {
	if s.navigatorClient == nil {
		return []string{"navigator"}, errors.New("navigator client not configured")
	}
	switch row.action {
	case "ensure":
		resp, err := s.navigatorClient.EnsureCustomDomain(ctx, &dnspb.EnsureCustomDomainRequest{
			TenantId: row.tenantID,
			Domain:   row.domain,
		})
		if err != nil {
			return []string{"navigator"}, err
		}
		if resp == nil {
			return []string{"navigator"}, errors.New("navigator ensure custom domain returned nil response")
		}
		if !resp.GetAccepted() || resp.GetError() != "" {
			return []string{"navigator"}, fmt.Errorf("navigator ensure custom domain rejected: %s", resp.GetError())
		}
	case "remove":
		resp, err := s.navigatorClient.RemoveCustomDomain(ctx, &dnspb.RemoveCustomDomainRequest{
			TenantId: row.tenantID,
			Domain:   row.domain,
		})
		if err != nil {
			return []string{"navigator"}, err
		}
		if resp == nil {
			return []string{"navigator"}, errors.New("navigator remove custom domain returned nil response")
		}
		if !resp.GetAccepted() {
			return []string{"navigator"}, errors.New("navigator remove custom domain rejected")
		}
	default:
		return []string{"navigator"}, fmt.Errorf("unsupported action %q", row.action)
	}
	return nil, nil
}
