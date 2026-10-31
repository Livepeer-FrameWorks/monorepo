package main

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
)

// Inbox payloads have a format owned by their stored kind. Provider callbacks
// include form data; internal reconciliation records carry tenant headers and
// JSON UUIDs, matching the producers that feed the indexed operator inventory.
func providerInboxSeedValue(column string, row int) (string, bool) {
	provider := []string{"stripe", "mollie", "checkout_recovery", "tier_upgrade", "refund_settlement", "tenant_closure"}[row%6]
	id := fmt.Sprintf("00000000-0000-4000-8000-%012x", row+1)
	switch column {
	case "provider":
		return provider, true
	case "headers":
		if provider == "stripe" {
			return `{"Stripe-Signature":"fixture","Content-Type":"application/json"}`, true
		}
		if provider == "mollie" {
			return `{"Content-Type":"application/x-www-form-urlencoded"}`, true
		}
		if provider != "refund_settlement" {
			return fmt.Sprintf(`{"tenant_id":%q}`, id), true
		}
		return fmt.Sprintf(`{"tenant_id":%q,"reference_id":%q,"provider":"stripe","reference_kind":"checkout","invoice_id":"in_fixture","provider_payment_id":"pi_fixture","currency":"EUR","amount_cents":"1200"}`, id, id), true
	case "raw_payload":
		var payload []byte
		switch provider {
		case "mollie":
			payload = []byte(fmt.Sprintf("id=tr_fixture_%d", row))
		case "stripe":
			payload = []byte(fmt.Sprintf(`{"id":%q,"type":"invoice.paid","data":{"object":{"metadata":{"tenant_id":%q}}}}`, id, id))
		default:
			payload = []byte(fmt.Sprintf(`{"id":%q,"tenant_id":%q,"checkout_reservation_id":%q}`, id, id, id))
		}
		return `\x` + hex.EncodeToString(payload), true
	default:
		return "", false
	}
}

// Bind internal inventory rows to actual live purchases after tombstone seeding,
// so tenant-scoped explains execute receipt enrichment instead of empty joins.
func (s *seeder) bindPurserInboxReceipts(ctx context.Context, conn *sql.Conn) error {
	if s.tables["purser.provider_webhook_inbox"] == nil {
		return nil
	}
	for _, purchase := range []struct {
		kind, table, receiptKey string
	}{
		{"checkout_recovery", "purser.payment_provider_intents", "checkout_reservation_id"},
		{"tier_upgrade", "purser.subscription_tier_upgrades", "id"},
	} {
		if s.tables[purchase.table] == nil {
			continue
		}
		query := fmt.Sprintf(`WITH receipts AS MATERIALIZED (
    SELECT id, tenant_id, ROW_NUMBER() OVER (ORDER BY id) AS position FROM %s
), callbacks AS MATERIALIZED (
    SELECT id, ROW_NUMBER() OVER (ORDER BY id) AS position
    FROM purser.provider_webhook_inbox WHERE provider = $1
)
UPDATE purser.provider_webhook_inbox inbox
SET headers = inbox.headers || jsonb_build_object('tenant_id', receipts.tenant_id::text),
    raw_payload = convert_to(jsonb_build_object($2::text, receipts.id::text,
                            'tenant_id', receipts.tenant_id::text)::text, 'UTF8')
FROM callbacks JOIN receipts
  ON receipts.position = 1 + (callbacks.position - 1) %% NULLIF((SELECT COUNT(*) FROM receipts), 0)
WHERE inbox.id = callbacks.id`, purchase.table)
		if _, err := conn.ExecContext(ctx, query, purchase.kind, purchase.receiptKey); err != nil {
			return fmt.Errorf("bind %s explain receipts: %w", purchase.kind, err)
		}
	}
	return nil
}
