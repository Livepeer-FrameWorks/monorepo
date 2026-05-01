package pricing

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"frameworks/api_billing/internal/rating"
	pb "frameworks/pkg/proto"
)

// fakeQM is a minimal QuartermasterClient stub for tests. It returns the
// (owner_tenant_id, is_platform_official) pair the test wants without
// touching the real gRPC stack.
type fakeQM struct {
	clusters map[string]*pb.InfrastructureCluster
}

func (f *fakeQM) GetCluster(_ context.Context, clusterID string) (*pb.ClusterResponse, error) {
	c, ok := f.clusters[clusterID]
	if !ok {
		return nil, errors.New("cluster not found")
	}
	return &pb.ClusterResponse{Cluster: c}, nil
}

func dec(s string) decimal.Decimal {
	d, err := decimal.NewFromString(s)
	if err != nil {
		panic(err)
	}
	return d
}

// tierRulesEUR returns a representative tier rule set for tests.
func tierRulesEUR() []rating.Rule {
	return []rating.Rule{
		{
			Meter:            rating.MeterDeliveredMinutes,
			Model:            rating.ModelTieredGraduated,
			Currency:         "EUR",
			IncludedQuantity: dec("120000"),
			UnitPrice:        dec("0.00055"),
		},
		{
			Meter:            rating.MeterAverageStorageGB,
			Model:            rating.ModelTieredGraduated,
			Currency:         "EUR",
			IncludedQuantity: dec("100"),
			UnitPrice:        dec("0.02"),
		},
	}
}

// expectNoHistoryRow registers a sqlmock expectation that the history
// SELECT returns sql.ErrNoRows for the given cluster.
func expectNoHistoryRow(mock sqlmock.Sqlmock, clusterID string) {
	mock.ExpectQuery(regexp.QuoteMeta("FROM purser.cluster_pricing_history")).
		WithArgs(clusterID, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"version_id", "pricing_model", "currency", "base_price", "metered_rates"}))
}

// expectHistoryRow registers a sqlmock expectation that the history SELECT
// returns one row with the given fields.
func expectHistoryRow(mock sqlmock.Sqlmock, clusterID, model, currency, basePrice, meteredRatesJSON string) uuid.UUID {
	versionID := uuid.New()
	mock.ExpectQuery(regexp.QuoteMeta("FROM purser.cluster_pricing_history")).
		WithArgs(clusterID, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"version_id", "pricing_model", "currency", "base_price", "metered_rates"}).
			AddRow(versionID.String(), model, currency, basePrice, meteredRatesJSON))
	return versionID
}

// TestEquivalence_TierInherit_NoHistoryRow asserts that for a platform-official
// cluster with no cluster_pricing row, the resolver returns the tenant's tier
// rules unchanged. This is the core legacy-compat invariant.
func TestEquivalence_TierInherit_NoHistoryRow(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	tenantID := uuid.New().String()
	clusterID := "central-primary"

	qm := &fakeQM{clusters: map[string]*pb.InfrastructureCluster{
		clusterID: {ClusterId: clusterID, IsPlatformOfficial: true},
	}}

	expectNoHistoryRow(mock, clusterID)

	tierRules := tierRulesEUR()
	got, err := ResolveClusterPricing(context.Background(), ResolveInputs{
		DB: db, QM: qm,
		ConsumingTenantID: tenantID,
		ClusterID:         clusterID,
		AsOf:              time.Now(),
		TierRules:         tierRules,
		TierCurrency:      "EUR",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if got.Kind != KindPlatformOfficial {
		t.Errorf("Kind = %s, want platform_official", got.Kind)
	}
	if got.Model != ModelTierInherit {
		t.Errorf("Model = %s, want tier_inherit", got.Model)
	}
	if got.PricingSource != SourceTier {
		t.Errorf("PricingSource = %s, want tier", got.PricingSource)
	}
	if !rulesEqual(got.MeteredRules, tierRules) {
		t.Errorf("MeteredRules differ from input tier rules:\n got=%+v\n want=%+v", got.MeteredRules, tierRules)
	}
	if got.Currency != "EUR" {
		t.Errorf("Currency = %q, want EUR", got.Currency)
	}
	if got.PriceVersionID != uuid.Nil {
		t.Errorf("PriceVersionID = %s, want zero (no history row)", got.PriceVersionID)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestTenantPrivate_NoHistoryRow_DefaultsSelfHosted(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	tenantID := uuid.New()
	clusterID := "self-hosted-no-row"
	ownerStr := tenantID.String()

	qm := &fakeQM{clusters: map[string]*pb.InfrastructureCluster{
		clusterID: {ClusterId: clusterID, OwnerTenantId: &ownerStr},
	}}
	expectNoHistoryRow(mock, clusterID)

	got, err := ResolveClusterPricing(context.Background(), ResolveInputs{
		DB: db, QM: qm,
		ConsumingTenantID: tenantID.String(),
		ClusterID:         clusterID,
		AsOf:              time.Now(),
		TierRules:         tierRulesEUR(),
		TierCurrency:      "EUR",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Kind != KindTenantPrivate {
		t.Errorf("Kind = %s, want tenant_private", got.Kind)
	}
	if got.Model != ModelFreeUnmetered {
		t.Errorf("Model = %s, want free_unmetered", got.Model)
	}
	if got.PricingSource != SourceSelfHosted {
		t.Errorf("PricingSource = %s, want self_hosted", got.PricingSource)
	}
	for _, r := range got.MeteredRules {
		if !r.UnitPrice.IsZero() {
			t.Errorf("rule %s unit price = %s, want 0", r.Meter, r.UnitPrice)
		}
	}
}

func TestThirdParty_NoHistoryRow_FailsClosed(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	consumer := uuid.New()
	owner := uuid.New()
	clusterID := "marketplace-no-row"
	ownerStr := owner.String()

	qm := &fakeQM{clusters: map[string]*pb.InfrastructureCluster{
		clusterID: {ClusterId: clusterID, OwnerTenantId: &ownerStr},
	}}
	expectNoHistoryRow(mock, clusterID)

	_, err = ResolveClusterPricing(context.Background(), ResolveInputs{
		DB: db, QM: qm,
		ConsumingTenantID: consumer.String(),
		ClusterID:         clusterID,
		AsOf:              time.Now(),
		TierRules:         tierRulesEUR(),
		TierCurrency:      "EUR",
	})
	if !errors.Is(err, ErrThirdPartyPricingMissing) {
		t.Fatalf("err = %v, want ErrThirdPartyPricingMissing", err)
	}
}

// TestEquivalence_TierInherit_RatingMatches is the CI-gated invariant: for a
// tier_inherit cluster, the rating Result computed via the resolver path must
// be identical to the legacy direct LoadEffectiveTier+Rate path. This locks
// in the "no behavior change for existing tenants" guarantee.
func TestEquivalence_TierInherit_RatingMatches(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	tenantID := uuid.New().String()
	clusterID := "central-primary"
	asOf := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	qm := &fakeQM{clusters: map[string]*pb.InfrastructureCluster{
		clusterID: {ClusterId: clusterID, IsPlatformOfficial: true},
	}}
	expectNoHistoryRow(mock, clusterID)

	tierRules := tierRulesEUR()
	resolved, err := ResolveClusterPricing(context.Background(), ResolveInputs{
		DB: db, QM: qm,
		ConsumingTenantID: tenantID,
		ClusterID:         clusterID,
		AsOf:              asOf,
		TierRules:         tierRules,
		TierCurrency:      "EUR",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	usage := map[rating.Meter]decimal.Decimal{
		rating.MeterDeliveredMinutes: dec("250000"),
		rating.MeterAverageStorageGB: dec("75"),
	}

	legacy, err := rating.Rate(rating.Input{
		Currency:    "EUR",
		BasePrice:   dec("19.99"),
		Rules:       tierRules,
		Usage:       usage,
		PeriodStart: asOf,
		PeriodEnd:   asOf.AddDate(0, 1, 0),
	})
	if err != nil {
		t.Fatalf("legacy rate: %v", err)
	}

	derived, err := rating.Rate(rating.Input{
		Currency:    resolved.Currency,
		BasePrice:   dec("19.99"),
		Rules:       resolved.MeteredRules,
		Usage:       usage,
		PeriodStart: asOf,
		PeriodEnd:   asOf.AddDate(0, 1, 0),
	})
	if err != nil {
		t.Fatalf("derived rate: %v", err)
	}

	if !legacy.TotalAmount.Equal(derived.TotalAmount) {
		t.Errorf("TotalAmount diverges: legacy=%s derived=%s", legacy.TotalAmount, derived.TotalAmount)
	}
	if !legacy.UsageAmount.Equal(derived.UsageAmount) {
		t.Errorf("UsageAmount diverges: legacy=%s derived=%s", legacy.UsageAmount, derived.UsageAmount)
	}
	if len(legacy.UsageLines) != len(derived.UsageLines) {
		t.Fatalf("UsageLines length: legacy=%d derived=%d", len(legacy.UsageLines), len(derived.UsageLines))
	}
	for i := range legacy.UsageLines {
		l, d := legacy.UsageLines[i], derived.UsageLines[i]
		if l.LineKey != d.LineKey || !l.Amount.Equal(d.Amount) || !l.Quantity.Equal(d.Quantity) {
			t.Errorf("line %d diverges:\n legacy=%+v\n derived=%+v", i, l, d)
		}
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestKindClassification covers all three derivation paths plus the
// fail-closed ambiguous case. A cluster with neither is_platform_official
// nor owner_tenant_id MUST surface as an error so the invoice routes to
// manual_review instead of silently defaulting to platform-official.
func TestKindClassification(t *testing.T) {
	consumingTenant := uuid.New()
	otherTenant := uuid.New()

	cases := []struct {
		name         string
		ownerTenant  *uuid.UUID
		platformFlag bool
		want         ClusterKind
		wantErr      error
	}{
		{"platform_official always wins", &consumingTenant, true, KindPlatformOfficial, nil},
		{"no owner with no platform flag fails closed", nil, false, "", ErrAmbiguousClusterOwnership},
		{"owner == consuming tenant → tenant_private", &consumingTenant, false, KindTenantPrivate, nil},
		{"owner != consuming tenant → third_party_marketplace", &otherTenant, false, KindThirdPartyMarketplace, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := classify(ownership{
				OwnerTenantID:      tc.ownerTenant,
				IsPlatformOfficial: tc.platformFlag,
			}, consumingTenant.String())
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("classify err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("classify err = %v, want nil", err)
			}
			if got != tc.want {
				t.Errorf("classify = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestFreeUnmetered_TenantPrivate covers the zero-priced informational-line
// rule for self-hosted clusters: rules are non-empty but priced at zero, and
// the source label is self_hosted.
func TestFreeUnmetered_TenantPrivate(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	consumingTenant := uuid.New()
	clusterID := "self-hosted-edge-1"
	ownerStr := consumingTenant.String()

	qm := &fakeQM{clusters: map[string]*pb.InfrastructureCluster{
		clusterID: {ClusterId: clusterID, OwnerTenantId: &ownerStr, IsPlatformOfficial: false},
	}}
	expectHistoryRow(mock, clusterID, "free_unmetered", "EUR", "0.00", "{}")

	tierRules := tierRulesEUR()
	got, err := ResolveClusterPricing(context.Background(), ResolveInputs{
		DB: db, QM: qm,
		ConsumingTenantID: consumingTenant.String(),
		ClusterID:         clusterID,
		AsOf:              time.Now(),
		TierRules:         tierRules,
		TierCurrency:      "EUR",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if got.Kind != KindTenantPrivate {
		t.Errorf("Kind = %s, want tenant_private", got.Kind)
	}
	if got.PricingSource != SourceSelfHosted {
		t.Errorf("PricingSource = %s, want self_hosted", got.PricingSource)
	}
	if len(got.MeteredRules) != len(tierRules) {
		t.Fatalf("MeteredRules len = %d, want %d (zero-priced lines must still appear)", len(got.MeteredRules), len(tierRules))
	}
	for _, r := range got.MeteredRules {
		if !r.UnitPrice.IsZero() {
			t.Errorf("rule for %s: UnitPrice = %s, want 0", r.Meter, r.UnitPrice)
		}
		if !r.IncludedQuantity.IsZero() {
			t.Errorf("rule for %s: IncludedQuantity = %s, want 0 (no quotas hide self-hosted usage)", r.Meter, r.IncludedQuantity)
		}
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestMetered_ThirdPartyMarketplace covers a third-party operator cluster
// charging its own per-meter rate. Operator credit is exercised in the
// operator package's tests; this test only verifies the resolver wiring.
func TestMetered_ThirdPartyMarketplace(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	consumingTenant := uuid.New()
	otherTenant := uuid.New()
	clusterID := "operator-edge-eu-1"
	ownerStr := otherTenant.String()

	qm := &fakeQM{clusters: map[string]*pb.InfrastructureCluster{
		clusterID: {ClusterId: clusterID, OwnerTenantId: &ownerStr},
	}}
	expectHistoryRow(mock, clusterID, "metered", "EUR", "0.00",
		`{"delivered_minutes":{"unit_price":"0.00050","model":"all_usage"}}`)

	got, err := ResolveClusterPricing(context.Background(), ResolveInputs{
		DB: db, QM: qm,
		ConsumingTenantID: consumingTenant.String(),
		ClusterID:         clusterID,
		AsOf:              time.Now(),
		TierRules:         tierRulesEUR(),
		TierCurrency:      "EUR",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if got.Kind != KindThirdPartyMarketplace {
		t.Errorf("Kind = %s, want third_party_marketplace", got.Kind)
	}
	if got.PricingSource != SourceClusterMetered {
		t.Errorf("PricingSource = %s, want cluster_metered", got.PricingSource)
	}
	if got.OwnerTenantID == nil || got.OwnerTenantID.String() != otherTenant.String() {
		t.Errorf("OwnerTenantID = %v, want %s", got.OwnerTenantID, otherTenant)
	}
	if len(got.MeteredRules) != 1 || got.MeteredRules[0].Meter != rating.MeterDeliveredMinutes {
		t.Errorf("MeteredRules = %+v", got.MeteredRules)
	}
	if !got.MeteredRules[0].UnitPrice.Equal(dec("0.00050")) {
		t.Errorf("UnitPrice = %s, want 0.00050", got.MeteredRules[0].UnitPrice)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestCustom_MissingRates returns the sentinel error so the writer can route
// the invoice to manual_review. Per the plan this is fail-closed, not partial.
func TestCustom_MissingRates(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	consumingTenant := uuid.New()
	otherTenant := uuid.New()
	clusterID := "custom-no-rates"
	ownerStr := otherTenant.String()

	qm := &fakeQM{clusters: map[string]*pb.InfrastructureCluster{
		clusterID: {ClusterId: clusterID, OwnerTenantId: &ownerStr},
	}}
	expectHistoryRow(mock, clusterID, "custom", "EUR", "0.00", "{}")

	_, err = ResolveClusterPricing(context.Background(), ResolveInputs{
		DB: db, QM: qm,
		ConsumingTenantID: consumingTenant.String(),
		ClusterID:         clusterID,
		AsOf:              time.Now(),
		TierRules:         tierRulesEUR(),
		TierCurrency:      "EUR",
	})
	if !errors.Is(err, ErrCustomPricingMissingForCluster) {
		t.Fatalf("err = %v, want ErrCustomPricingMissingForCluster", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestMonthly_AccessOnly covers Decision 7: monthly clusters do not produce
// usage invoice lines this pass — usage rates as zero with the
// included_subscription source.
func TestMonthly_AccessOnly(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	consumingTenant := uuid.New()
	otherTenant := uuid.New()
	clusterID := "monthly-cluster"
	ownerStr := otherTenant.String()

	qm := &fakeQM{clusters: map[string]*pb.InfrastructureCluster{
		clusterID: {ClusterId: clusterID, OwnerTenantId: &ownerStr},
	}}
	expectHistoryRow(mock, clusterID, "monthly", "EUR", "49.00", "{}")

	got, err := ResolveClusterPricing(context.Background(), ResolveInputs{
		DB: db, QM: qm,
		ConsumingTenantID: consumingTenant.String(),
		ClusterID:         clusterID,
		AsOf:              time.Now(),
		TierRules:         tierRulesEUR(),
		TierCurrency:      "EUR",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if got.PricingSource != SourceIncludedSubscription {
		t.Errorf("PricingSource = %s, want included_subscription", got.PricingSource)
	}
	for _, r := range got.MeteredRules {
		if !r.UnitPrice.IsZero() {
			t.Errorf("rule for %s: UnitPrice = %s, want 0 (monthly is access-only)", r.Meter, r.UnitPrice)
		}
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestZeroPricedProcessingEmitsLine is the regression test for the
// self-hosted/free-transcoding visibility bug: zeroPricedRulesFromTier
// must convert codec_multiplier to all_usage AND the writer must populate
// Usage[processing_seconds] so the converted rule emits a line. Without
// either change, free transcoding silently disappears from invoices.
func TestZeroPricedProcessingEmitsLine(t *testing.T) {
	tierRules := []rating.Rule{
		{
			Meter:     rating.MeterProcessingSeconds,
			Model:     rating.ModelCodecMultiplier,
			Currency:  "EUR",
			UnitPrice: dec("0.001"),
			Config: map[string]any{
				"codec_multipliers": map[string]any{"h264": 1.0, "hevc": 1.5},
			},
		},
	}
	zeroed := zeroPricedRulesFromTier(tierRules, "EUR")
	if len(zeroed) != 1 {
		t.Fatalf("zeroed rule count = %d, want 1", len(zeroed))
	}
	if zeroed[0].Model != rating.ModelAllUsage {
		t.Fatalf("zeroed model = %s, want all_usage (codec_multiplier returns nil at unit_price=0)", zeroed[0].Model)
	}

	// Now drive the rating engine with the zeroed rule and a non-zero
	// processing_seconds Usage value. The line MUST emit at $0.00 — that
	// is the customer-visible "self-hosted: 0.00" invariant.
	res, err := rating.Rate(rating.Input{
		Currency:  "EUR",
		BasePrice: decimal.Zero,
		Rules:     zeroed,
		Usage: map[rating.Meter]decimal.Decimal{
			rating.MeterProcessingSeconds: dec("3600"),
		},
	})
	if err != nil {
		t.Fatalf("rate: %v", err)
	}
	if len(res.UsageLines) != 1 {
		t.Fatalf("UsageLines = %d, want 1 (zero-price line must NOT be dropped)", len(res.UsageLines))
	}
	if !res.UsageLines[0].Amount.IsZero() {
		t.Errorf("Amount = %s, want 0", res.UsageLines[0].Amount)
	}
	if !res.UsageLines[0].Quantity.Equal(dec("3600")) {
		t.Errorf("Quantity = %s, want 3600 (informational quantity must reach the line)", res.UsageLines[0].Quantity)
	}
}

// rulesEqual is value equality on rule slices, treating decimal.Decimal
// equality via .Equal.
func rulesEqual(a, b []rating.Rule) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Meter != b[i].Meter ||
			a[i].Model != b[i].Model ||
			a[i].Currency != b[i].Currency ||
			!a[i].IncludedQuantity.Equal(b[i].IncludedQuantity) ||
			!a[i].UnitPrice.Equal(b[i].UnitPrice) {
			return false
		}
	}
	return true
}
