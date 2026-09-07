package datamigrations

import (
	"context"
	"fmt"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billingentitlements"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"
)

const TenantDNSEntitlementsID = "quartermaster_tenant_dns_entitlements_v0_3_0"
const tenantDNSEntitlementHandoffKey = billingentitlements.TenantDNSHandoffKey

const tenantDNSEntitlementHandoffCompleteSQL = `
SELECT EXISTS (
	SELECT 1
	FROM quartermaster.billing_entitlement_handoffs
	WHERE handoff_key = $1
)`

const backfillTenantDNSEntitlementsSQL = `
WITH batch AS (
	SELECT id
	FROM quartermaster.tenants
	WHERE billing_entitlements_observed_at = 'epoch'::timestamptz
	ORDER BY id
    LIMIT $1
    FOR UPDATE SKIP LOCKED
)
UPDATE quartermaster.tenants tenant
	SET custom_subdomain_enabled = false,
	custom_domain_enabled = false,
	billing_entitlements_observed_at = NOW(),
	updated_at = NOW()
FROM batch
WHERE tenant.id = batch.id`

const countUnobservedTenantDNSEntitlementsSQL = `
SELECT COUNT(*)
FROM quartermaster.tenants
WHERE billing_entitlements_observed_at = 'epoch'::timestamptz`

func registerTenantDNSEntitlementsMigration() {
	datamigrate.Register(datamigrate.Migration{
		ID:                  TenantDNSEntitlementsID,
		Service:             "quartermaster",
		IntroducedIn:        "v0.3.0",
		RequiredBeforePhase: "postdeploy",
		Description:         "close remaining unobserved DNS grants after Purser materializes subscription entitlements",
		Run:                 runTenantDNSEntitlements,
		Verify:              verifyTenantDNSEntitlements,
	})
}

func runTenantDNSEntitlements(ctx context.Context, db datamigrate.DB, opts datamigrate.RunOptions) (datamigrate.Progress, error) {
	var handoffComplete bool
	if err := db.QueryRowContext(ctx, tenantDNSEntitlementHandoffCompleteSQL, tenantDNSEntitlementHandoffKey).Scan(&handoffComplete); err != nil {
		return datamigrate.Progress{}, fmt.Errorf("check Purser DNS entitlement handoff: %w", err)
	}
	if !handoffComplete {
		return datamigrate.Progress{}, fmt.Errorf("purser DNS entitlement handoff incomplete: durable sweep receipt %q is missing", tenantDNSEntitlementHandoffKey)
	}

	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = 1000
	}
	result, err := db.ExecContext(ctx, backfillTenantDNSEntitlementsSQL, batchSize)
	if err != nil {
		return datamigrate.Progress{}, fmt.Errorf("backfill tenant DNS entitlements: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return datamigrate.Progress{}, fmt.Errorf("count backfilled tenant DNS entitlements: %w", err)
	}
	done := false
	if changed < int64(batchSize) {
		var remaining int64
		if err := db.QueryRowContext(ctx, countUnobservedTenantDNSEntitlementsSQL).Scan(&remaining); err != nil {
			return datamigrate.Progress{}, fmt.Errorf("count remaining tenant DNS entitlements: %w", err)
		}
		done = remaining == 0
	}
	return datamigrate.Progress{Scanned: changed, Changed: changed, Done: done}, nil
}

func verifyTenantDNSEntitlements(ctx context.Context, db datamigrate.DB) error {
	var count int64
	if err := db.QueryRowContext(ctx, countUnobservedTenantDNSEntitlementsSQL).Scan(&count); err != nil {
		return fmt.Errorf("verify tenant DNS entitlement backfill: %w", err)
	}
	if count != 0 {
		return fmt.Errorf("tenant DNS entitlement backfill has %d unobserved rows remaining", count)
	}
	return nil
}
