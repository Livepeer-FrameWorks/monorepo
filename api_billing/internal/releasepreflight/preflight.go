// Package releasepreflight previews Purser's release effects without starting workers.
package releasepreflight

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const Limit = 100

type Finding struct {
	TenantID string `json:"tenant_id"`
	ID       string `json:"id"`
}
type Check struct {
	ID     string    `json:"id"`
	Status string    `json:"status"`
	Effect string    `json:"effect"`
	Total  int64     `json:"total"`
	Rows   []Finding `json:"rows"`
}
type Report struct {
	Release    string    `json:"release"`
	ObservedAt time.Time `json:"observed_at"`
	Checks     []Check   `json:"checks"`
}

type checkSpec struct{ id, effect, table, query string }

// JSON field access projects an absent expand column as NULL. These checks only
// use that projection for columns whose expand default is NULL (or false).
var checks = []checkSpec{
	{"fallback_reason_constraint", "rows that fail chk_subscription_free_fallback_reason", "tenant_subscriptions", `SELECT r.tenant_id::text, r.id::text FROM purser.tenant_subscriptions r WHERE (to_jsonb(r)->>'free_fallback_reason') NOT IN ('payment_failed','cancelled')`},
	{"collection_writeoff_constraint", "rows that fail chk_billing_collection_writeoff_reason", "billing_collection_writeoffs", `SELECT r.tenant_id::text, r.id::text FROM purser.billing_collection_writeoffs r WHERE r.reason NOT IN ('account_closed','moved_to_free','operator_approved')`},
	{"invoice_pricing_source_constraint", "rows that fail chk_invoice_line_items_pricing_source", "invoice_line_items", `SELECT i.tenant_id::text, r.id::text FROM purser.invoice_line_items r JOIN purser.billing_invoices i ON i.id=r.invoice_id WHERE (to_jsonb(r)->>'pricing_source') NOT IN ('tier','cluster_metered','cluster_monthly','cluster_custom','free_unmetered','self_hosted','included_subscription','beta_free','grant_waived')`},
	{"grant_fallback_reference", "rows that fail the fallback tier foreign key", "subscription_operator_grants", `SELECT s.tenant_id::text, r.subscription_id::text AS id FROM purser.subscription_operator_grants r JOIN purser.tenant_subscriptions s ON s.id=r.subscription_id WHERE to_jsonb(r)->>'fallback_tier_id' IS NOT NULL AND NOT EXISTS (SELECT 1 FROM purser.billing_tiers t WHERE t.id::text=to_jsonb(r)->>'fallback_tier_id')`},
	{"grant_assigned_reference", "rows that fail the assigned tier foreign key", "subscription_operator_grants", `SELECT s.tenant_id::text, r.subscription_id::text AS id FROM purser.subscription_operator_grants r JOIN purser.tenant_subscriptions s ON s.id=r.subscription_id WHERE to_jsonb(r)->>'assigned_tier_id' IS NOT NULL AND NOT EXISTS (SELECT 1 FROM purser.billing_tiers t WHERE t.id::text=to_jsonb(r)->>'assigned_tier_id')`},
	{"unknown_submission_state", "existing intents defaulting to unknown (or still unknown after expand)", "payment_provider_intents", `SELECT r.tenant_id::text, r.id::text FROM purser.payment_provider_intents r WHERE COALESCE(to_jsonb(r)->>'submission_state','unknown')='unknown'`},
	{"free_fallback_first_batch", "first 100 due fallback candidates; workers recheck holds before changing a tenant", "tenant_subscriptions", `SELECT subscription.tenant_id::text, subscription.id::text FROM purser.tenant_subscriptions subscription
 WHERE (to_jsonb(subscription)->>'free_fallback_at')::timestamptz <= CURRENT_TIMESTAMP
 AND to_jsonb(subscription)->>'free_fallback_moved_at' IS NULL
 AND ((to_jsonb(subscription)->>'free_fallback_attempted_at')::timestamptz IS NULL OR (to_jsonb(subscription)->>'free_fallback_attempted_at')::timestamptz <= CURRENT_TIMESTAMP-INTERVAL '5 minutes')
 AND NOT EXISTS (SELECT 1 FROM purser.tenant_billing_closures c WHERE c.tenant_id=subscription.tenant_id)
 AND NOT EXISTS (SELECT 1 FROM purser.subscription_operator_grants g WHERE g.subscription_id=subscription.id)
 ORDER BY (to_jsonb(subscription)->>'free_fallback_attempted_at')::timestamptz ASC NULLS FIRST, (to_jsonb(subscription)->>'free_fallback_at')::timestamptz, subscription.tenant_id LIMIT 100`},
	{"provider_tier_sync", "tenants whose first discovery pass queues provider tier work; provider success is not predicted", "tenant_subscriptions", `SELECT subscription.tenant_id::text, subscription.id::text FROM purser.tenant_subscriptions subscription
 JOIN purser.billing_tiers target ON target.id=NULLIF(to_jsonb(subscription)->>'pending_tier_id','')::uuid
 WHERE to_jsonb(subscription)->>'pending_tier_id' IS NOT NULL
 AND to_jsonb(subscription)->>'pending_reason'='downgrade'
 AND to_jsonb(subscription)->>'pending_provider_tier_id' IS DISTINCT FROM to_jsonb(subscription)->>'pending_tier_id'
 AND (NULLIF(subscription.stripe_subscription_id,'') IS NOT NULL OR NULLIF(subscription.mollie_subscription_id,'') IS NOT NULL)
 AND NOT COALESCE((to_jsonb(subscription)->>'free_fallback_provider_stopping')::boolean,false)
 AND to_jsonb(subscription)->>'free_fallback_provider_ended_at' IS NULL
 AND to_jsonb(subscription)->>'free_fallback_at' IS NULL
 AND (target.base_price > 0 OR NOT COALESCE(target.is_default_postpaid,false)
      OR (to_jsonb(subscription)->>'pending_effective_at')::timestamptz <= CURRENT_TIMESTAMP+INTERVAL '72 hours')
 AND NOT EXISTS (SELECT 1 FROM purser.provider_webhook_inbox inbox WHERE inbox.provider='tier_downgrade' AND inbox.event_key='tier-downgrade:' || subscription.tenant_id::text AND inbox.status <> 'processed')`},
}

func Run(ctx context.Context, db *sql.DB) (*Report, error) {
	// Raw transaction: the preview requires one read-only repeatable-read snapshot and never commits.
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // The preview always rolls back its read-only transaction.
	if _, err = tx.ExecContext(ctx, `SET LOCAL statement_timeout='20s'`); err != nil {
		return nil, err
	}
	out := &Report{Release: "v0.3.14", Checks: []Check{}}
	if err = tx.QueryRowContext(ctx, `SELECT CURRENT_TIMESTAMP`).Scan(&out.ObservedAt); err != nil {
		return nil, err
	}
	for _, spec := range checks {
		item := Check{ID: spec.id, Status: "checked", Effect: spec.effect, Rows: []Finding{}}
		var present bool
		if err = tx.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, "purser."+spec.table).Scan(&present); err != nil {
			return nil, err
		}
		if spec.id == "free_fallback_first_batch" {
			var ready bool
			if err = tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='purser' AND table_name='tenant_subscriptions' AND column_name='free_fallback_at')`).Scan(&ready); err != nil {
				return nil, err
			}
			if !ready {
				item.Effect += "; expand adds NULL schedules, so no existing schedules are due"
				out.Checks = append(out.Checks, item)
				continue
			}
		}
		if !present {
			item.Status = "unavailable"
			out.Checks = append(out.Checks, item)
			continue
		}
		// Only fixed service-owned SQL is composed here; the caller supplies no SQL.
		query := `SELECT tenant_id, id, count(*) OVER () FROM (` + spec.query + `) findings ORDER BY tenant_id,id LIMIT $1`
		item.Rows, item.Total, err = readFindings(ctx, tx, query)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", spec.id, err)
		}
		out.Checks = append(out.Checks, item)
	}
	return out, nil
}

func readFindings(ctx context.Context, tx *sql.Tx, query string) ([]Finding, int64, error) {
	rows, err := tx.QueryContext(ctx, query, Limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	findings := []Finding{}
	var total int64
	for rows.Next() {
		var f Finding
		if err = rows.Scan(&f.TenantID, &f.ID, &total); err != nil {
			return nil, 0, err
		}
		findings = append(findings, f)
	}
	return findings, total, rows.Err()
}
