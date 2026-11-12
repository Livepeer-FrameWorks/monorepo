//go:build schema_verify

package datamigrations

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/google/uuid"
)

func TestNativeSettlementExpandDoesNotRewriteLegacyEvidence_RealPG(t *testing.T) {
	db := startPreContractPurserRealPG(t)
	tenant := uuid.NewString()
	_, err := db.ExecContext(t.Context(), `ALTER TABLE purser.provider_settlements DROP CONSTRAINT chk_provider_settlements_state;
 DROP TRIGGER trg_provider_settlement_identity_valuation ON purser.provider_settlements`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(t.Context(), `INSERT INTO purser.provider_settlements(tenant_id,provider,provider_payment_id,provider_balance_transaction_id,charge_amount_cents,charge_currency,settled_amount_cents,fee_cents,net_cents,settlement_currency,status,settled_at)
 VALUES($1,'stripe','legacy-eur','legacy-eur-transaction',100,'EUR',100,5,95,'EUR','settled',NOW())`, tenant)
	if err != nil {
		t.Fatal(err)
	}
	expand, err := dbsql.Content.ReadFile("migrations/purser/v0.3.15/expand/001_native_provider_settlements.sql")
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err = db.ExecContext(t.Context(), string(expand)); err != nil {
			t.Fatal(err)
		}
	}
	var missing int
	if err = db.QueryRowContext(t.Context(), `SELECT count(*) FROM purser.provider_settlements WHERE tenant_id=$1 AND settled_eur_cents IS NULL`, tenant).Scan(&missing); err != nil {
		t.Fatal(err)
	}
	if missing != 1 {
		t.Fatal("expand rewrote historical evidence instead of leaving the tenant-scoped background migration its work")
	}
}

func TestProviderSettlementIdentityBackfill_RealPG(t *testing.T) {
	verifyProviderSettlementBackfillContract(t)
}
func TestProviderSettlementIdentityBackfill_RealYugabyte(t *testing.T) {
	verifyProviderSettlementBackfillContract(t)
}

func verifyProviderSettlementBackfillContract(t *testing.T) {
	t.Helper()
	var db *sql.DB
	if strings.Contains(t.Name(), "_RealYugabyte") {
		var ok bool
		db, ok = dockerpg.OpenSharedYugabyteBaseline(t, "settlement_identity_backfill", "purser")
		if !ok {
			t.Skip("requires the shared Yugabyte fixture")
		}
	} else {
		db = startPreContractPurserRealPG(t)
	}
	ctx := t.Context()
	if _, err := db.ExecContext(ctx, `ALTER TABLE purser.provider_settlements DROP CONSTRAINT chk_provider_settlements_state;
 DROP TRIGGER trg_provider_settlement_identity_valuation ON purser.provider_settlements`); err != nil {
		t.Fatal(err)
	}
	tenant, other := uuid.NewString(), uuid.NewString()
	insert := func(owner, payment string) {
		t.Helper()
		_, err := db.ExecContext(ctx, `INSERT INTO purser.provider_settlements
 (tenant_id,provider,provider_payment_id,provider_balance_transaction_id,charge_amount_cents,charge_currency,settled_amount_cents,fee_cents,net_cents,settlement_currency,status,settled_at)
 VALUES($1,'stripe',$2,$2,100,'EUR',100,5,95,'EUR','settled','2026-11-11T23:30:00-05:00')`, owner, payment)
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 5; i++ {
		insert(tenant, fmt.Sprintf("legacy-%d", i))
	}
	insert(other, "other-legacy")
	expand, err := dbsql.Content.ReadFile("migrations/purser/v0.3.15/expand/001_native_provider_settlements.sql")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := db.ExecContext(ctx, string(expand)); err != nil {
			t.Fatal(err)
		}
	}
	post, err := dbsql.Content.ReadFile("migrations/purser/v0.3.15/postdeploy/001_validate_native_provider_settlements.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, string(post)); err == nil {
		t.Fatal("postdeploy validated before historical identity valuation")
	}
	migration := ProviderSettlementIdentityBackfill()
	if migration.IntroducedIn != "v0.3.15" || migration.RequiredBeforePhase != "postdeploy" {
		t.Fatal("missing release phase barrier")
	}
	if err := migration.Verify(ctx, db); err == nil {
		t.Fatal("incomplete legacy valuation passed verification")
	}
	scopes, err := migration.Scopes(ctx, db)
	if err != nil || len(scopes) != 2 {
		t.Fatalf("historical tenant inventory: %+v %v", scopes, err)
	}
	// An older writer can continue settling EUR while the historical batches run.
	insert(tenant, "concurrent-old-writer")
	opts := datamigrate.RunOptions{Scope: datamigrate.ScopeKey{Kind: "tenant", Value: tenant}, BatchSize: 2, DryRun: true}
	preview, err := migration.Run(ctx, db, opts)
	if err != nil || preview.Changed != 5 || !preview.Done {
		t.Fatalf("preview=%+v %v", preview, err)
	}
	missing := func(owner string) int {
		t.Helper()
		var n int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM purser.provider_settlements WHERE tenant_id=$1 AND settled_eur_cents IS NULL`, owner).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if missing(tenant) != 5 {
		t.Fatal("dry run wrote identity valuation")
	}
	opts.DryRun = false
	first, err := migration.Run(ctx, db, opts)
	if err != nil || first.Changed != 2 || first.Done || missing(tenant) != 3 || missing(other) != 1 {
		t.Fatalf("unbounded or cross-tenant batch: %+v %v", first, err)
	}
	for {
		progress, err := migration.Run(ctx, db, opts)
		if err != nil {
			t.Fatal(err)
		}
		if progress.Done {
			break
		}
	}
	if err := migration.Verify(ctx, db); err == nil {
		t.Fatal("one completed tenant hid another pending owner")
	}
	opts.Scope.Value = other
	if progress, err := migration.Run(ctx, db, opts); err != nil || progress.Changed != 1 || !progress.Done {
		t.Fatalf("other owner batch=%+v %v", progress, err)
	}
	if err := migration.Verify(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, string(post)); err != nil {
		t.Fatal(err)
	}
	var valid int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM purser.provider_settlements WHERE tenant_id=$1
 AND settled_amount_cents=100 AND fee_cents=5 AND net_cents=95 AND settlement_currency='EUR'
 AND settled_eur_cents=100 AND fee_eur_cents=5 AND net_eur_cents=95 AND valuation_units_per_eur=1
 AND valuation_source='identity' AND valuation_reference_date=DATE '2026-11-12'`, tenant).Scan(&valid); err != nil || valid != 6 {
		t.Fatalf("identity valuation changed native evidence or UTC date: %d %v", valid, err)
	}
	if progress, err := migration.Run(ctx, db, opts); err != nil || progress.Changed != 0 || !progress.Done {
		t.Fatalf("replay=%+v %v", progress, err)
	}
	scopes, err = migration.Scopes(ctx, db)
	if err != nil || len(scopes) != 1 || !scopes[0].IsZero() {
		t.Fatalf("completed inventory=%+v %v", scopes, err)
	}
	if progress, err := migration.Run(ctx, db, datamigrate.RunOptions{}); err != nil || !progress.Done {
		t.Fatalf("empty database completion=%+v %v", progress, err)
	}
}
