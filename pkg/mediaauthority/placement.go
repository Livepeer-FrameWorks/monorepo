package mediaauthority

import (
	"fmt"
	"math"
	"strings"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
)

func supportedSchema(version uint32) bool {
	return version == SchemaVersion || IsPlacementSchema(version)
}

// IsPlacementSchema reports whether a payload schema carries placement policy.
// Schema 3 differs from schema 2 only in admitting node selectors.
func IsPlacementSchema(version uint32) bool {
	return version == PlacementSchemaVersion || version == NodePlacementSchemaVersion
}

// validateNodeSelectorSchema keeps node IDs out of schema-2 payloads: a reader
// that attested only schema 2 rejects the unknown selector field outright.
func validateNodeSelectorSchema(version uint32, set *placementpb.PolicySet) error {
	if version != NodePlacementSchemaVersion && placement.PolicySetHasNodeSelectors(set) {
		return fmt.Errorf("%w: node placement selectors require schema %d", ErrUnknownSchema, NodePlacementSchemaVersion)
	}
	return nil
}

func validateTenantPlacement(envelope *mediaauthoritypb.AuthorityEnvelope, tenant *mediaauthoritypb.TenantAuthority) error {
	if tenant.GetSchemaVersion() == SchemaVersion {
		if tenant.GetMediaPlacement() != nil {
			return fmt.Errorf("%w: tenant placement requires schema 2", ErrUnknownSchema)
		}
		for _, grant := range tenant.GetEffectiveClusterGrants() {
			if grant.GetRegionId() != "" || grant.GetMediaConsent() != nil || grant.GetCommercialFacts() != nil {
				return fmt.Errorf("%w: placement grant facts require schema 2", ErrUnknownSchema)
			}
		}
		return nil
	}
	if tenant.GetMediaPlacement() == nil {
		return fmt.Errorf("%w: schema 2 requires an explicit tenant placement revision", ErrMalformed)
	}
	if err := placement.ValidatePolicySet(tenant.GetMediaPlacement()); err != nil {
		return fmt.Errorf("%w: tenant placement: %w", ErrMalformed, err)
	}
	if err := validateNodeSelectorSchema(tenant.GetSchemaVersion(), tenant.GetMediaPlacement()); err != nil {
		return err
	}
	for _, grant := range tenant.GetEffectiveClusterGrants() {
		if grant.GetMediaConsent() == nil || grant.GetMediaConsent().GetRevision() > math.MaxInt64 {
			return fmt.Errorf("%w: placement grant requires owner consent", ErrMalformed)
		}
		if len(grant.GetRegionId()) > maxIdentifierBytes || strings.TrimSpace(grant.GetRegionId()) != grant.GetRegionId() {
			return fmt.Errorf("%w: invalid placement region", ErrMalformed)
		}
		facts := grant.GetCommercialFacts()
		if facts == nil {
			continue
		}
		if facts.GetIngestPrice() != nil || facts.GetServePrice() != nil || len(facts.GetIngestPrices()) != 0 || len(facts.GetServePrices()) != 0 {
			return fmt.Errorf("%w: comparison prices require object-scoped quotes", ErrMalformed)
		}
		if facts.GetCharging() != placementpb.Charging_CHARGING_RATED && facts.GetCharging() != placementpb.Charging_CHARGING_PERMANENTLY_FREE {
			return fmt.Errorf("%w: invalid charging classification", ErrMalformed)
		}
		if strings.TrimSpace(facts.GetRevision()) == "" || len(facts.GetRevision()) > maxIdentifierBytes ||
			facts.GetExpiresAt() == nil || !facts.GetExpiresAt().IsValid() || envelope.GetValidUntil().AsTime().After(facts.GetExpiresAt().AsTime()) {
			return fmt.Errorf("%w: authority outlives commercial facts or lacks their revision", ErrMalformed)
		}
		if err := placement.ValidateCommercialPrices(facts); err != nil {
			return fmt.Errorf("%w: %w", ErrMalformed, err)
		}
	}
	return nil
}

func validateObjectPlacement(envelope *mediaauthoritypb.AuthorityEnvelope, object *mediaauthoritypb.MediaObjectAuthority) error {
	if object.GetSchemaVersion() == SchemaVersion {
		if object.GetMediaPlacement() != nil || object.GetPlacementTenantRevision() != 0 || len(object.GetCommercialQuotes()) != 0 {
			return fmt.Errorf("%w: object placement requires schema 2", ErrUnknownSchema)
		}
		return nil
	}
	if object.GetMediaPlacement() == nil || object.GetPlacementTenantRevision() > math.MaxInt64 {
		return fmt.Errorf("%w: schema 2 requires explicit object placement and a bounded parent revision", ErrMalformed)
	}
	if err := placement.ValidatePolicySet(object.GetMediaPlacement()); err != nil {
		return fmt.Errorf("%w: object placement: %w", ErrMalformed, err)
	}
	if err := validateNodeSelectorSchema(object.GetSchemaVersion(), object.GetMediaPlacement()); err != nil {
		return err
	}
	return validateObjectCommercialQuotes(envelope, object)
}

// EffectivePlacement combines already verified authorities only when they describe
// the same tenant intent. A mixed delivery cannot silently fall back to old defaults.
func EffectivePlacement(tenant *mediaauthoritypb.TenantAuthority, object *mediaauthoritypb.MediaObjectAuthority, verb placement.Verb) (*placement.Policy, error) {
	if tenant == nil || object == nil || tenant.GetTenantId() == "" || tenant.GetTenantId() != object.GetTenantId() {
		return nil, fmt.Errorf("%w: placement authority identity mismatch", ErrMalformed)
	}
	if tenant.GetSchemaVersion() != object.GetSchemaVersion() || !supportedSchema(tenant.GetSchemaVersion()) {
		return nil, fmt.Errorf("%w: placement authorities use different schemas", ErrUnknownSchema)
	}
	if IsPlacementSchema(tenant.GetSchemaVersion()) && (tenant.GetMediaPlacement() == nil || object.GetMediaPlacement() == nil ||
		tenant.GetMediaPlacement().GetRevision() != object.GetPlacementTenantRevision()) {
		return nil, fmt.Errorf("%w: placement parent revision mismatch", ErrMalformed)
	}
	return placement.CompilePolicySets(tenant.GetMediaPlacement(), object.GetMediaPlacement(), verb)
}
