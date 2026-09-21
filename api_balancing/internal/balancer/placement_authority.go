package balancer

import (
	"errors"
	"slices"
	"strings"
	"time"

	localauthority "frameworks/api_balancing/internal/mediaauthority"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
)

var (
	ErrPlacementAuthorityNotReady = errors.New("placement authority is not ready")
	ErrPlacementAuthorityDenied   = errors.New("placement authority denies admission")
	ErrPlacementAuthorityInvalid  = errors.New("placement authority is inconsistent")
)

// PlacementAuthority is a credential-free projection of verified signed state.
// Cells includes unreachable owners: runtime reachability cannot change the census.
type PlacementAuthority struct {
	TenantID               string
	ObjectID               string
	InternalName           string
	Verb                   placement.Verb
	Policy                 *placement.Policy
	PolicyDigest           string
	PolicyRevision         uint64
	ParentRevision         uint64
	TenantAuthorityVersion int64
	ObjectAuthorityVersion int64
	ExpiresAt              time.Time
	Clusters               map[string]PlacementClusterFacts
	Cells                  []PlacementCell
	ObjectKind             mediaauthoritypb.MediaObjectKind
	IngestMode             string
	SourceGrants           map[string]PlacementSourceGrant
	CommercialQuote        *placementpb.CommercialQuoteResponse
}

// Source grants are independent of destination preferences. An ingest-only
// cluster may supply its publisher to an entitled serving destination.
// AllowPrivatePullSources is the signed per-cluster consent a configured pull
// source needs before a cluster may dial an RFC1918 or otherwise private
// upstream; it is unrelated to a destination's external-source consent.
type PlacementSourceGrant struct {
	CellID                  string
	AllowIngest             bool
	AllowServe              bool
	AllowPrivatePullSources bool
}

// CompilePlacementAuthority does not confer activation or grant access. It
// requires independently promoted placement-schema snapshots (tenant and object on
// the same schema) and preserves owner consent and commercial provenance
// separately from the consuming tenant's preferences.
func CompilePlacementAuthority(pair localauthority.PlacementPair, verb placement.Verb, now time.Time) (PlacementAuthority, error) {
	if verb != placement.Ingest && verb != placement.Serve {
		return PlacementAuthority{}, ErrPlacementAuthorityInvalid
	}
	ready := pair.Tenant.Ready && pair.Object.Ready
	if verb == placement.Ingest {
		ready = pair.Tenant.IngestReady && pair.Object.IngestReady
		if pair.Object.Authority.GetLiveStream().GetIngestMode() != "push" {
			ready = ready && pair.Tenant.SourceReady && pair.Object.SourceReady
		}
	}
	return compilePlacementAuthority(pair, verb, now, ready)
}

// CompileSourceDialAuthority compiles the ingest policy a node must satisfy
// before it dials a configured source. The dial paths already require source
// readiness before they trust the sealed source, so that is the readiness this
// projection demands; ingest readiness belongs to publisher admission.
func CompileSourceDialAuthority(pair localauthority.PlacementPair, now time.Time) (PlacementAuthority, error) {
	return compilePlacementAuthority(pair, placement.Ingest, now, pair.Tenant.SourceReady && pair.Object.SourceReady)
}

func compilePlacementAuthority(pair localauthority.PlacementPair, verb placement.Verb, now time.Time, ready bool) (PlacementAuthority, error) {
	tenant, object := pair.Tenant.Authority, pair.Object.Authority
	if tenant == nil || object == nil || !sharedauthority.IsPlacementSchema(tenant.GetSchemaVersion()) || tenant.GetSchemaVersion() != object.GetSchemaVersion() {
		return PlacementAuthority{}, ErrPlacementAuthorityNotReady
	}
	// Freshness also carries the cell's restore fence: a pair it withholds reads
	// hard-expired while its signed validity still runs.
	if !ready || now.IsZero() || pair.Tenant.Version <= 0 || pair.Object.Version <= 0 ||
		!now.Before(pair.Tenant.ValidUntil) || !now.Before(pair.Object.ValidUntil) ||
		pair.Tenant.Freshness == localauthority.FreshnessHardExpired || pair.Object.Freshness == localauthority.FreshnessHardExpired {
		return PlacementAuthority{}, ErrPlacementAuthorityNotReady
	}
	if tenant.GetLifecycle() != mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE ||
		object.GetLifecycle() != mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE ||
		tenant.GetBillingDecision() != mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW {
		return PlacementAuthority{}, ErrPlacementAuthorityDenied
	}
	if object.GetInternalName() == "" || pair.Object.AuthorityID == "" ||
		(verb == placement.Ingest && object.GetObjectKind() != mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM) {
		return PlacementAuthority{}, ErrPlacementAuthorityInvalid
	}
	policy, err := sharedauthority.EffectivePlacement(tenant, object, verb)
	if err != nil {
		return PlacementAuthority{}, ErrPlacementAuthorityInvalid
	}
	digest, err := placement.Digest(policy)
	if err != nil {
		return PlacementAuthority{}, ErrPlacementAuthorityInvalid
	}
	result := PlacementAuthority{
		TenantID: tenant.GetTenantId(), ObjectID: pair.Object.AuthorityID, InternalName: object.GetInternalName(), Verb: verb,
		Policy: policy, PolicyDigest: digest, PolicyRevision: object.GetMediaPlacement().GetRevision(), ParentRevision: tenant.GetMediaPlacement().GetRevision(),
		TenantAuthorityVersion: pair.Tenant.Version, ObjectAuthorityVersion: pair.Object.Version,
		ExpiresAt: pair.Tenant.ValidUntil, Clusters: make(map[string]PlacementClusterFacts),
		ObjectKind: object.GetObjectKind(), IngestMode: object.GetLiveStream().GetIngestMode(), SourceGrants: make(map[string]PlacementSourceGrant),
	}
	if pair.Object.ValidUntil.Before(result.ExpiresAt) {
		result.ExpiresAt = pair.Object.ValidUntil
	}
	quote, quoteErr := sharedauthority.PlacementCommercialQuote(tenant, object, verb, now)
	if quoteErr != nil {
		return PlacementAuthority{}, ErrPlacementAuthorityInvalid
	}
	commercial := make(map[string]*placementpb.CommercialFacts)
	if quote != nil {
		result.CommercialQuote = quote
		if quote.GetExpiresAt().AsTime().Before(result.ExpiresAt) {
			result.ExpiresAt = quote.GetExpiresAt().AsTime()
		}
		for _, cluster := range quote.GetClusters() {
			commercial[cluster.GetClusterId()] = cluster.GetFacts()
		}
	}
	grants := tenant.GetEffectiveClusterGrants()
	if len(grants) > 4096 {
		return PlacementAuthority{}, ErrPlacementAuthorityInvalid
	}
	seen := make(map[string]bool, len(grants))
	cells := make(map[string][]string)
	for _, grant := range grants {
		clusterID, cellID := grant.GetClusterId(), grant.GetControlCellId()
		if clusterID == "" || cellID == "" || seen[clusterID] || strings.TrimSpace(clusterID) != clusterID || strings.TrimSpace(cellID) != cellID ||
			grant.GetSubscriptionStatus() != "active" || grant.GetMediaConsent() == nil {
			return PlacementAuthority{}, ErrPlacementAuthorityInvalid
		}
		seen[clusterID] = true
		if expiry := grant.GetExpiresAt(); expiry != nil && (!expiry.IsValid() || pair.Tenant.ValidUntil.After(expiry.AsTime())) {
			return PlacementAuthority{}, ErrPlacementAuthorityInvalid
		}
		facts, err := placementGrantFacts(grant, commercial[clusterID], verb, result.ExpiresAt)
		if err != nil {
			return PlacementAuthority{}, err
		}
		result.SourceGrants[clusterID] = PlacementSourceGrant{CellID: cellID,
			AllowIngest: grant.GetMediaConsent().GetAllowIngest(), AllowServe: grant.GetMediaConsent().GetAllowServe(),
			AllowPrivatePullSources: grant.GetAllowPrivatePullSources()}
		if !slices.Contains(facts.AllowedVerbs, verb) {
			continue
		}
		result.Clusters[clusterID] = facts
		cells[cellID] = append(cells[cellID], clusterID)
	}
	if len(cells) > placementMaxCells {
		return PlacementAuthority{}, ErrPlacementAuthorityInvalid
	}
	for id, clusters := range cells {
		slices.Sort(clusters)
		result.Cells = append(result.Cells, PlacementCell{ID: id, ClusterIDs: clusters})
	}
	slices.SortFunc(result.Cells, func(a, b PlacementCell) int { return strings.Compare(a.ID, b.ID) })
	return result, nil
}

func placementGrantFacts(grant *mediaauthoritypb.TenantClusterGrant, commercial *placementpb.CommercialFacts, verb placement.Verb, expiresAt time.Time) (PlacementClusterFacts, error) {
	facts := PlacementClusterFacts{
		OwnerTenantID: grant.GetOwnerTenantId(), Official: grant.GetClusterClass() == "platform_official", Region: grant.GetRegionId(),
		AuthorityUntil: expiresAt, ConsentRevision: grant.GetMediaConsent().GetRevision(), AllowExternalSource: grant.GetMediaConsent().GetAllowExternalSource(),
		Charging: placement.ChargingUnknown,
	}
	switch grant.GetClusterClass() {
	case "platform_official", "tenant_private", "third_party_marketplace":
	default:
		return PlacementClusterFacts{}, ErrPlacementAuthorityInvalid
	}
	facts.AllowedVerbs = placement.ConsentVerbs(grant.GetMediaConsent())
	classification := grant.GetCommercialFacts()
	if classification.GetIngestPrice() != nil || classification.GetServePrice() != nil || len(classification.GetIngestPrices()) != 0 || len(classification.GetServePrices()) != 0 {
		return PlacementClusterFacts{}, ErrPlacementAuthorityInvalid
	}
	if commercial == nil {
		commercial = classification
	}
	if commercial == nil {
		return facts, nil
	}
	if commercial.GetRevision() == "" || commercial.GetExpiresAt() == nil || !commercial.GetExpiresAt().IsValid() || expiresAt.After(commercial.GetExpiresAt().AsTime()) || placement.ValidateCommercialPrices(commercial) != nil {
		return PlacementClusterFacts{}, ErrPlacementAuthorityInvalid
	}
	switch commercial.GetCharging() {
	case placementpb.Charging_CHARGING_RATED:
		facts.Charging = placement.Rated
	case placementpb.Charging_CHARGING_PERMANENTLY_FREE:
		facts.Charging = placement.PermanentlyFree
	default:
		return PlacementClusterFacts{}, ErrPlacementAuthorityInvalid
	}
	facts.ChargingRevision, facts.ChargingUntil = commercial.GetRevision(), commercial.GetExpiresAt().AsTime()
	price := commercial.GetServePrice()
	prices := commercial.GetServePrices()
	if verb == placement.Ingest {
		price = commercial.GetIngestPrice()
		prices = commercial.GetIngestPrices()
	}
	if price != nil {
		facts.Price = &placement.Price{AmountMicros: price.GetAmountMicros(), Currency: price.GetCurrency(), Unit: price.GetUnit(), Revision: commercial.GetRevision(), ExpiresAt: facts.ChargingUntil}
	}
	for _, quote := range prices {
		facts.Prices = append(facts.Prices, placement.Price{AmountMicros: quote.GetAmountMicros(), Currency: quote.GetCurrency(), Unit: quote.GetUnit(), Revision: commercial.GetRevision(), ExpiresAt: facts.ChargingUntil})
	}
	return facts, nil
}

// MatchesQuery binds a peer's requested view to this cell's independent policy
// evaluation. A service credential is not permission to request another cell's
// nodes or replace tenant policy with coordinator-supplied rules.
func (authority PlacementAuthority) MatchesQuery(query *placementpb.CandidateQuery, cellID string) bool {
	if placement.ValidateCandidateQuery(query) != nil || query.GetTenantId() != authority.TenantID || query.GetObjectId() != authority.ObjectID ||
		query.GetInternalName() != authority.InternalName || query.GetPolicyDigest() != authority.PolicyDigest || query.GetPolicyRevision() != authority.PolicyRevision ||
		query.GetParentRevision() != authority.ParentRevision {
		return false
	}
	if (authority.Verb == placement.Ingest && query.GetVerb() != placementpb.Verb_VERB_INGEST) || (authority.Verb == placement.Serve && query.GetVerb() != placementpb.Verb_VERB_SERVE) {
		return false
	}
	for _, cell := range authority.Cells {
		if cell.ID != cellID {
			continue
		}
		if len(query.GetClusterIds()) != len(cell.ClusterIDs) {
			return false
		}
		for _, clusterID := range query.GetClusterIds() {
			if !slices.Contains(cell.ClusterIDs, clusterID) {
				return false
			}
		}
		return true
	}
	return false
}
