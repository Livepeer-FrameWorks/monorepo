package mediaauthority

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
)

type SourceSnapshot struct {
	Object MediaObjectSnapshot
	Tenant TenantSnapshot
	Secret *mediaauthoritypb.LiveStreamSecret
}

// PullSource loads the signed object, tenant decision, and cell-sealed source
// descriptor without consulting any central service. found distinguishes a
// never-delivered object from an unusable/denied delivered authority.
func (s *Store) PullSource(ctx context.Context, internalName string) (SourceSnapshot, bool, error) {
	object, err := s.MediaObjectSourceByInternalName(ctx, internalName)
	if errors.Is(err, sql.ErrNoRows) {
		return SourceSnapshot{}, false, nil
	}
	if err != nil {
		return SourceSnapshot{}, true, fmt.Errorf("read local source object: %w", err)
	}
	result := SourceSnapshot{Object: object}
	if object.Authority == nil || object.Authority.GetLiveStream() == nil || object.Authority.GetLiveStream().GetIngestMode() != "pull" {
		return result, true, errors.New("local source authority is not a pull stream")
	}
	tenant, err := s.TenantSource(ctx, object.Authority.GetTenantId())
	if err != nil {
		return result, true, fmt.Errorf("read local source tenant: %w", err)
	}
	result.Tenant = tenant
	if object.Authority.GetLifecycle() != mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE ||
		tenant.Authority == nil || tenant.Authority.GetLifecycle() != mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE ||
		tenant.Authority.GetBillingDecision() != mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW {
		return result, true, nil
	}
	secret, err := s.OpenLiveStreamSecret(object)
	if err != nil {
		return result, true, err
	}
	result.Secret = secret
	return result, true, nil
}

// ArtifactSource loads the signed artifact identity and tenant decision used
// to reopen a vod+ source. It deliberately uses the independent source
// readiness markers: playback readiness alone does not authorize a source
// implementation whose hash, kind, or origin has not been shadow-compared.
func (s *Store) ArtifactSource(ctx context.Context, internalName string) (SourceSnapshot, bool, error) {
	object, err := s.MediaObjectSourceByInternalName(ctx, internalName)
	if errors.Is(err, sql.ErrNoRows) {
		return SourceSnapshot{}, false, nil
	}
	if err != nil {
		return SourceSnapshot{}, true, fmt.Errorf("read local artifact source object: %w", err)
	}
	result := SourceSnapshot{Object: object}
	if object.Authority == nil || object.Authority.GetArtifact() == nil {
		return result, true, errors.New("local source authority is not an artifact")
	}
	tenant, err := s.TenantSource(ctx, object.Authority.GetTenantId())
	if err != nil {
		return result, true, fmt.Errorf("read local artifact source tenant: %w", err)
	}
	result.Tenant = tenant
	return result, true, nil
}

func SourceDecisionAllows(snapshot SourceSnapshot) bool {
	return snapshot.Object.Authority != nil && snapshot.Object.Authority.GetLifecycle() == mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE &&
		snapshot.Tenant.Authority != nil && snapshot.Tenant.Authority.GetLifecycle() == mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE &&
		snapshot.Tenant.Authority.GetBillingDecision() == mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW &&
		snapshot.Secret != nil && snapshot.Secret.GetSourceEnabled() && snapshot.Secret.GetSourceUri() != ""
}
