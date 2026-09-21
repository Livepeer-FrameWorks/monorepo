package commodoredb

import (
	"context"
	"database/sql"
	"strings"
	"time"

	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
)

// Refresh-obligation lanes. Each lane has its own worker, lease, and timeout;
// a target holds at most one obligation per lane.
const (
	MediaAuthorityLaneEvent = "event"
	// MediaAuthorityLaneBulk carries work enumerated from a set (a tenant's
	// objects, the tenants to reconcile) so it never queues in front of a change.
	MediaAuthorityLaneBulk           = "bulk"
	MediaAuthorityLaneObjectDeadline = "object_deadline"
	MediaAuthorityLaneTenantDeadline = "tenant_deadline"
)

const (
	MediaAuthorityTargetTenant             = "tenant"
	MediaAuthorityTargetLiveStream         = "live_stream"
	MediaAuthorityTargetArtifact           = "artifact"
	MediaAuthorityTargetTenantMediaObjects = "tenant_media_objects"
)

const (
	tenantTargetPrefix             = "tenant:"
	mediaObjectTargetPrefix        = "media_object:"
	tenantMediaObjectsTargetPrefix = "tenant_media_objects:"
)

// MediaAuthorityTarget names what a refresh obligation compiles. Key equals the
// compile-fence scope of the same authority, so an obligation and its fence
// always agree on identity.
type MediaAuthorityTarget struct {
	Key  string
	Kind string
}

func TenantMediaAuthorityTarget(tenantID string) MediaAuthorityTarget {
	return MediaAuthorityTarget{Key: tenantTargetPrefix + strings.TrimSpace(tenantID), Kind: MediaAuthorityTargetTenant}
}

func LiveStreamMediaAuthorityTarget(streamID string) MediaAuthorityTarget {
	return MediaAuthorityTarget{
		Key:  mediaObjectTargetPrefix + sharedauthority.LiveStreamAuthorityID(streamID),
		Kind: MediaAuthorityTargetLiveStream,
	}
}

// ArtifactMediaAuthorityTarget is keyed by artifact ID alone. A chapter and the
// VOD row backing it are one authority and must share one obligation.
func ArtifactMediaAuthorityTarget(artifactID string) MediaAuthorityTarget {
	return MediaAuthorityTarget{
		Key:  mediaObjectTargetPrefix + sharedauthority.ArtifactAuthorityID(artifactID),
		Kind: MediaAuthorityTargetArtifact,
	}
}

func TenantMediaObjectsAuthorityTarget(tenantID string) MediaAuthorityTarget {
	return MediaAuthorityTarget{
		Key:  tenantMediaObjectsTargetPrefix + strings.TrimSpace(tenantID),
		Kind: MediaAuthorityTargetTenantMediaObjects,
	}
}

// MediaAuthorityTargetForReason maps a refresh reason to its target. Reasons of
// the form media_object:<kind>:<id>:<cause> name one object,
// tenant_media_objects:<cause> names every object of the tenant, and anything
// else refreshes the tenant authority itself.
func MediaAuthorityTargetForReason(tenantID, reason string) MediaAuthorityTarget {
	reason = strings.TrimSpace(reason)
	if strings.HasPrefix(reason, tenantMediaObjectsTargetPrefix) {
		return TenantMediaObjectsAuthorityTarget(tenantID)
	}
	if parts := strings.SplitN(reason, ":", 4); len(parts) == 4 && parts[0] == "media_object" && strings.TrimSpace(parts[2]) != "" {
		switch parts[1] {
		case "live_stream":
			return LiveStreamMediaAuthorityTarget(parts[2])
		case "clip", "dvr", "vod", "chapter":
			return ArtifactMediaAuthorityTarget(parts[2])
		}
	}
	return TenantMediaAuthorityTarget(tenantID)
}

// ObjectID returns the stream or artifact ID of a media-object target.
func (t MediaAuthorityTarget) ObjectID() string {
	switch t.Kind {
	case MediaAuthorityTargetLiveStream:
		return strings.TrimPrefix(t.Key, mediaObjectTargetPrefix+sharedauthority.LiveStreamAuthorityID(""))
	case MediaAuthorityTargetArtifact:
		return strings.TrimPrefix(t.Key, mediaObjectTargetPrefix+sharedauthority.ArtifactAuthorityID(""))
	default:
		return ""
	}
}

// EnqueueMediaAuthorityEvent records that a target's source state changed. Any
// number of events for one target fold into its single event-lane obligation.
func (q *Queries) EnqueueMediaAuthorityEvent(ctx context.Context, target MediaAuthorityTarget, tenantID, reason, sourceService, sourceEventID string) error {
	return q.EnqueueMediaAuthorityObligation(ctx, EnqueueMediaAuthorityObligationParams{
		Lane: MediaAuthorityLaneEvent, TargetKey: target.Key, TargetKind: target.Kind, TenantID: tenantID,
		Reason: reason, SourceService: sourceService, SourceEventID: sourceEventID,
	})
}

// EnqueueMediaAuthorityBulk is EnqueueMediaAuthorityEvent for work enumerated
// from a set. notBefore spreads a sweep over time; zero means due now.
func (q *Queries) EnqueueMediaAuthorityBulk(ctx context.Context, target MediaAuthorityTarget, tenantID, reason, sourceEventID string, notBefore time.Time) error {
	return q.EnqueueMediaAuthorityObligation(ctx, EnqueueMediaAuthorityObligationParams{
		Lane: MediaAuthorityLaneBulk, TargetKey: target.Key, TargetKind: target.Kind, TenantID: tenantID,
		Reason: reason, SourceService: "commodore", SourceEventID: sourceEventID,
		NextAttemptAt: sql.NullTime{Time: notBefore.UTC(), Valid: !notBefore.IsZero()},
	})
}

// ScheduleMediaAuthorityRenewal sets when a published version must be renewed.
// A later publication of the same authority replaces the schedule.
func (q *Queries) ScheduleMediaAuthorityRenewal(ctx context.Context, target MediaAuthorityTarget, tenantID string, version int64, renewAt time.Time) error {
	lane := MediaAuthorityLaneObjectDeadline
	if target.Kind == MediaAuthorityTargetTenant {
		lane = MediaAuthorityLaneTenantDeadline
	}
	return q.EnqueueMediaAuthorityObligation(ctx, EnqueueMediaAuthorityObligationParams{
		Lane: lane, TargetKey: target.Key, TargetKind: target.Kind, TenantID: tenantID,
		Reason: "renewal", SourceService: "commodore", SourceEventID: "renewal:" + target.Key,
		NextAttemptAt: sql.NullTime{Time: renewAt.UTC(), Valid: true}, BoundVersion: sql.NullInt64{Int64: version, Valid: version > 0},
	})
}
