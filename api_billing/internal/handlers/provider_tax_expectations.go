package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	billingstripe "frameworks/api_billing/internal/stripe"
)

// RetainProviderTaxExpectation records intended terms before a provider write.
// A failed synchronization must not make older provider tax metadata authoritative.
func RetainProviderTaxExpectation(ctx context.Context, db purserdb.DBTX, tenant string, p *billingstripe.TaxProfile) error {
	var assessment TaxAssessment
	if err := json.Unmarshal(p.Evidence, &assessment); err != nil {
		return err
	}
	snapshot, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return purserdb.New(db).RetainProviderTaxExpectation(ctx, purserdb.RetainProviderTaxExpectationParams{TenantID: tenant, ProfileSha256: p.Fingerprint(), AssessedAt: assessment.AssessedAt, Assessment: snapshot})
}

func providerTaxExpectationAtPoint(ctx context.Context, db purserdb.DBTX, tenant string, p billingstripe.TaxProfile, at time.Time) (json.RawMessage, bool, error) {
	expected, err := purserdb.New(db).GetProviderTaxExpectationAtPoint(ctx, purserdb.GetProviderTaxExpectationAtPointParams{TenantID: tenant, TaxPoint: at})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	var accepted billingstripe.TaxProfile
	if err = json.Unmarshal(expected, &accepted); err != nil {
		return nil, false, err
	}
	return expected, ProviderTaxTermsEquivalent(p, accepted), nil
}
