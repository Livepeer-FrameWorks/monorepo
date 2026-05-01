// Package pricing resolves a (tenant, cluster, period) tuple into the rating
// rules that apply for that consumption. It composes tenant tier resolution
// (api_billing/internal/billing.LoadEffectiveTier) with the per-cluster
// purser.cluster_pricing model and Quartermaster cluster ownership data.
//
// The resolver is read-only and side-effect-free.
package pricing

import (
	"errors"

	"github.com/google/uuid"

	"frameworks/api_billing/internal/rating"
)

// ClusterKind classifies a cluster relative to the tenant consuming it. It is
// derived at resolution time from Quartermaster's is_platform_official and
// owner_tenant_id, plus the consuming tenant's id.
type ClusterKind string

const (
	// KindPlatformOfficial — owned/operated by FrameWorks. Legacy default.
	KindPlatformOfficial ClusterKind = "platform_official"
	// KindTenantPrivate — owned by the consuming tenant (self-hosted infra).
	// Zero media charge AND zero operator payout.
	KindTenantPrivate ClusterKind = "tenant_private"
	// KindThirdPartyMarketplace — owned by another tenant (operator). Generates
	// operator credit and platform fee.
	KindThirdPartyMarketplace ClusterKind = "third_party_marketplace"
)

// PricingSource records why a line was priced the way it was, for both audit
// and presentation. Stored on invoice_line_items.pricing_source.
type PricingSource string

const (
	SourceTier                 PricingSource = "tier"
	SourceClusterMetered       PricingSource = "cluster_metered"
	SourceClusterMonthly       PricingSource = "cluster_monthly"
	SourceClusterCustom        PricingSource = "cluster_custom"
	SourceFreeUnmetered        PricingSource = "free_unmetered"
	SourceSelfHosted           PricingSource = "self_hosted"
	SourceIncludedSubscription PricingSource = "included_subscription"
)

// Model mirrors purser.cluster_pricing.pricing_model. Kept as a typed string
// rather than reusing rating.Model since the two enums describe different
// concepts (cluster pricing model vs rating math model).
type Model string

const (
	ModelTierInherit   Model = "tier_inherit"
	ModelMetered       Model = "metered"
	ModelMonthly       Model = "monthly"
	ModelFreeUnmetered Model = "free_unmetered"
	ModelCustom        Model = "custom"
)

// ClusterPricing is the resolver's output: everything the rating writer needs
// to fan out per-cluster lines for one (tenant, cluster, period) tuple.
type ClusterPricing struct {
	// Model is the cluster's configured pricing_model.
	Model Model

	// Kind is the derived cluster classification for the consuming tenant.
	Kind ClusterKind

	// Currency is ISO 4217 — always populated.
	Currency string

	// MeteredRules are the rules the rating engine should apply to this
	// cluster's usage_records. Empty for monthly-access-only clusters.
	// For free_unmetered clusters this is non-empty with unit_price=0 (NOT
	// an empty slice) so usage still produces an informational invoice line.
	MeteredRules []rating.Rule

	// PricingSource is the per-line source label the writer should stamp on
	// every cluster usage line emitted from MeteredRules.
	PricingSource PricingSource

	// OwnerTenantID is the cluster owner from Quartermaster, nil when the
	// cluster has no owner (platform-managed). Drives operator credit
	// attribution.
	OwnerTenantID *uuid.UUID

	// IsPlatformOfficial mirrors Quartermaster's flag — surfaced so the
	// fee policy lookup can skip a Quartermaster round-trip.
	IsPlatformOfficial bool

	// PriceVersionID is the cluster_pricing_history.version_id snapshot
	// effective at the resolution timestamp. Stamped on every line so a
	// later mid-period repricing remains auditable. uuid.Nil for clusters
	// that have never had an explicit pricing config (legacy default).
	PriceVersionID uuid.UUID
}

// ErrCustomPricingMissingForCluster is returned by ResolveClusterPricing for
// clusters configured as Model=custom that have no metered_rates set. Callers
// must route the invoice to status='manual_review' and halt the rest of
// finalization. Never partial-finalize.
var ErrCustomPricingMissingForCluster = errors.New("pricing: cluster has custom model but no metered_rates configured")

// ErrAmbiguousClusterOwnership is returned when a cluster has neither
// is_platform_official nor an owner_tenant_id. Treating it as either
// platform or operator-owned would be a guess that affects who pays and
// who gets paid; callers route the invoice to manual_review instead.
var ErrAmbiguousClusterOwnership = errors.New("pricing: cluster has neither is_platform_official nor owner_tenant_id; cannot classify")

// ErrThirdPartyPricingMissing is returned when a marketplace cluster has no
// explicit Purser pricing history. Falling back to a tenant tier would bill at
// the wrong commercial model and create operator credits from the wrong price.
var ErrThirdPartyPricingMissing = errors.New("pricing: third-party marketplace cluster has no explicit pricing configured")
