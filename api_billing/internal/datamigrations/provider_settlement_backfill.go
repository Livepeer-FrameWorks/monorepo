package datamigrations

import (
	"context"
	"fmt"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"
	"github.com/google/uuid"
)

const ProviderSettlementIdentityBackfillID = "purser_settlement_identity_backfill_v0_3_15"

// ProviderSettlementIdentityBackfill materializes the identity valuation of
// historical EUR settlements. The expand trigger covers concurrent old writers.
func ProviderSettlementIdentityBackfill() datamigrate.Migration {
	return datamigrate.Migration{
		ID: ProviderSettlementIdentityBackfillID, Service: "purser", IntroducedIn: "v0.3.15", RequiredBeforePhase: "postdeploy",
		Description: "record EUR identity valuation for historical native EUR settlements before validating settlement evidence",
		Scopes:      providerSettlementBackfillScopes, Run: runProviderSettlementBackfill, Verify: verifyProviderSettlementBackfill,
	}
}

func providerSettlementBackfillScopes(ctx context.Context, db datamigrate.DB) ([]datamigrate.ScopeKey, error) {
	// This service-owned migration inventory spans tenants; each write is scoped.
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT tenant_id::text FROM purser.provider_settlements
 WHERE status='settled' AND settlement_currency='EUR' AND settled_eur_cents IS NULL ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var scopes []datamigrate.ScopeKey
	for rows.Next() {
		var tenant string
		if scanErr := rows.Scan(&tenant); scanErr != nil {
			return nil, scanErr
		}
		scopes = append(scopes, datamigrate.ScopeKey{Kind: "tenant", Value: tenant})
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, rowsErr
	}
	if len(scopes) == 0 {
		return []datamigrate.ScopeKey{{}}, nil
	}
	return scopes, nil
}

func runProviderSettlementBackfill(ctx context.Context, db datamigrate.DB, opts datamigrate.RunOptions) (datamigrate.Progress, error) {
	if opts.Scope.IsZero() {
		return datamigrate.Progress{Done: true}, verifyProviderSettlementBackfill(ctx, db)
	}
	if opts.Scope.Kind != "tenant" {
		return datamigrate.Progress{}, fmt.Errorf("settlement identity backfill requires a tenant scope")
	}
	if _, err := uuid.Parse(opts.Scope.Value); err != nil {
		return datamigrate.Progress{}, fmt.Errorf("invalid settlement tenant scope: %w", err)
	}
	if opts.DryRun {
		var count int64
		err := db.QueryRowContext(ctx, `SELECT count(*) FROM purser.provider_settlements WHERE tenant_id=$1::text::uuid
 AND status='settled' AND settlement_currency='EUR' AND settled_eur_cents IS NULL`, opts.Scope.Value).Scan(&count)
		return datamigrate.Progress{Scanned: count, Changed: count, Done: true}, err
	}
	batch := opts.BatchSize
	if batch <= 0 {
		batch = 500
	}
	batch = min(batch, 5000)
	result, err := db.ExecContext(ctx, `WITH pending AS (
 SELECT id FROM purser.provider_settlements WHERE tenant_id=$1::text::uuid
 AND status='settled' AND settlement_currency='EUR' AND settled_eur_cents IS NULL
 ORDER BY id LIMIT $2 FOR UPDATE
 ) UPDATE purser.provider_settlements settlement
 SET settled_eur_cents=settled_amount_cents,fee_eur_cents=fee_cents,net_eur_cents=net_cents,
 valuation_units_per_eur=1,valuation_reference_date=(settled_at AT TIME ZONE 'UTC')::date,valuation_source='identity'
 FROM pending WHERE settlement.tenant_id=$1::text::uuid AND settlement.id=pending.id
 AND settlement.status='settled' AND settlement.settlement_currency='EUR' AND settlement.settled_eur_cents IS NULL`, opts.Scope.Value, batch)
	if err != nil {
		return datamigrate.Progress{}, err
	}
	changed, err := result.RowsAffected()
	return datamigrate.Progress{Scanned: changed, Changed: changed, Done: changed < int64(batch)}, err
}

func verifyProviderSettlementBackfill(ctx context.Context, db datamigrate.DB) error {
	// The release-wide completion check probes the migration's partial index.
	var pending bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM purser.provider_settlements
 WHERE status='settled' AND settlement_currency='EUR' AND settled_eur_cents IS NULL)`).Scan(&pending); err != nil {
		return err
	}
	if pending {
		return fmt.Errorf("historical EUR settlements still need identity valuation before postdeploy")
	}
	return nil
}
