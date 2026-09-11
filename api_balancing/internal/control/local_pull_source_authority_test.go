package control

import (
	"errors"
	"testing"

	localauthority "frameworks/api_balancing/internal/mediaauthority"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
)

func activeSourceSnapshot() localauthority.SourceSnapshot {
	return localauthority.SourceSnapshot{
		Object: localauthority.MediaObjectSnapshot{
			Authority: &mediaauthoritypb.MediaObjectAuthority{
				TenantId:  "tenant-a",
				Lifecycle: mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
				Object: &mediaauthoritypb.MediaObjectAuthority_LiveStream{
					LiveStream: &mediaauthoritypb.LiveStreamAuthority{
						StreamId: "stream-1", IngestMode: "pull",
					},
				},
			},
			AuthorityID: "obj-1", Version: 4, SourceReady: true,
			Freshness: localauthority.FreshnessValid,
		},
		Tenant: localauthority.TenantSnapshot{
			Authority: &mediaauthoritypb.TenantAuthority{
				TenantId:        "tenant-a",
				Lifecycle:       mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
				BillingDecision: mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW,
			},
			Version: 2, SourceReady: true, Freshness: localauthority.FreshnessValid,
		},
		Secret: &mediaauthoritypb.LiveStreamSecret{
			SourceUri: "rtmp://origin.example/live", SourceEnabled: true,
			AllowedClusterIds: []string{"cluster-a", "cluster-b"},
		},
	}
}

// Once the local projection is marked, this cell answers pull-source questions
// itself and must never fall back to a central answer. So a marked snapshot
// yields a usable response, carrying the sealed source descriptor.
func TestLocalPullSourceResolutionServesMarkedAuthority(t *testing.T) {
	got, err := localPullSourceResolution(activeSourceSnapshot(), true, nil)
	if err != nil {
		t.Fatalf("marked authority was refused: %v", err)
	}
	if !got.Found || !got.Marked {
		t.Fatalf("found=%v marked=%v, want both", got.Found, got.Marked)
	}
	if got.Response == nil {
		t.Fatal("no response built, so the caller would consult the control plane for an answer it already holds")
	}
	if !got.Response.GetEnabled() || got.Response.GetTenantId() != "tenant-a" || got.Response.GetStreamId() != "stream-1" {
		t.Fatalf("response = %+v", got.Response)
	}
	if got.Response.GetSourceUri() != "rtmp://origin.example/live" {
		t.Fatalf("sealed source descriptor lost: %q", got.Response.GetSourceUri())
	}
	if len(got.Response.GetAllowedClusterIds()) != 2 {
		t.Fatalf("allowed clusters lost: %v", got.Response.GetAllowedClusterIds())
	}
}

// A denial is an answer, not an absence. A marked-but-disabled source must come
// back as Enabled=false rather than as no response, or the caller falls through
// to the control plane and the local denial is overridden by a stale allow.
func TestLocalPullSourceResolutionServesMarkedDenial(t *testing.T) {
	snapshot := activeSourceSnapshot()
	snapshot.Secret.SourceEnabled = false

	got, err := localPullSourceResolution(snapshot, true, nil)
	if err != nil {
		t.Fatalf("marked denial was reported as an error: %v", err)
	}
	if got.Response == nil {
		t.Fatal("a marked denial produced no response")
	}
	if got.Response.GetEnabled() {
		t.Fatal("a disabled source was reported as enabled")
	}
}

// An object marked ready before its tenant is an ordering violation: object
// readiness presupposes tenant readiness, so the reverse means the projection
// is inconsistent and must be reported rather than served.
func TestLocalPullSourceResolutionRefusesObjectReadyBeforeTenant(t *testing.T) {
	snapshot := activeSourceSnapshot()
	snapshot.Tenant.SourceReady = false

	got, err := localPullSourceResolution(snapshot, true, nil)
	if err == nil {
		t.Fatal("object-ready-before-tenant was accepted")
	}
	if got.Response != nil {
		t.Fatal("an inconsistent projection still produced a servable response")
	}
}

// The inverse ordering is normal, not corruption: a newly created second object
// under an already-ready tenant is tenant-ready and object-unready, and simply
// is not marked yet.
func TestLocalPullSourceResolutionTreatsUnmarkedObjectAsNotYetLocal(t *testing.T) {
	snapshot := activeSourceSnapshot()
	snapshot.Object.SourceReady = false

	got, err := localPullSourceResolution(snapshot, true, nil)
	if err != nil {
		t.Fatalf("tenant-ready/object-unready was treated as an error: %v", err)
	}
	if !got.Found {
		t.Fatal("a delivered authority was reported as never delivered")
	}
	if got.Marked {
		t.Fatal("an unmarked object was reported as marked")
	}
	if got.Response != nil {
		t.Fatal("an unmarked object produced a local answer instead of deferring")
	}
}

// Hard expiry is the end of local authority. Serving past it would let a cell
// keep honouring a decision the control plane may have revoked long ago.
func TestLocalPullSourceResolutionRefusesHardExpiry(t *testing.T) {
	for _, tc := range []struct {
		name  string
		mutch func(*localauthority.SourceSnapshot)
	}{
		{"object hard-expired", func(s *localauthority.SourceSnapshot) {
			s.Object.Freshness = localauthority.FreshnessHardExpired
		}},
		{"tenant hard-expired", func(s *localauthority.SourceSnapshot) {
			s.Tenant.Freshness = localauthority.FreshnessHardExpired
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := activeSourceSnapshot()
			tc.mutch(&snapshot)
			got, err := localPullSourceResolution(snapshot, true, nil)
			if err == nil {
				t.Fatal("a hard-expired authority was served")
			}
			if got.Response != nil {
				t.Fatal("a hard-expired authority produced a servable response")
			}
		})
	}
}

// A never-delivered object is not a denial: the caller must be free to ask the
// control plane, so no error and no response.
func TestLocalPullSourceResolutionPassesThroughWhenNotFound(t *testing.T) {
	got, err := localPullSourceResolution(localauthority.SourceSnapshot{}, false, nil)
	if err != nil {
		t.Fatalf("a missing local object was reported as an error: %v", err)
	}
	if got.Found || got.Marked || got.Response != nil {
		t.Fatalf("resolution = %+v, want an empty pass-through", got)
	}
}

// A read error on an unmarked object is not fatal — the caller can still ask
// the control plane. On a marked one it is, because there is no fallback left.
func TestLocalPullSourceResolutionPropagatesReadErrorOnlyWhenMarked(t *testing.T) {
	readErr := errors.New("sealed material unreadable")

	unmarked := activeSourceSnapshot()
	unmarked.Object.SourceReady = false
	if _, err := localPullSourceResolution(unmarked, true, readErr); err != nil {
		t.Fatalf("an unmarked read error blocked the control-plane fallback: %v", err)
	}

	got, err := localPullSourceResolution(activeSourceSnapshot(), true, readErr)
	if err == nil {
		t.Fatal("a marked read error was swallowed, leaving no answer and no error")
	}
	if got.Response != nil {
		t.Fatal("a failed read still produced a servable response")
	}
}

// A pull source may only be fetched from a cluster the tenant is granted, and a
// private source additionally requires that grant to allow private pulls —
// holding a grant is not the same as being allowed to reach inside it.
func TestLocalTenantAllowsSourceCluster(t *testing.T) {
	tenant := &mediaauthoritypb.TenantAuthority{
		EffectiveClusterGrants: []*mediaauthoritypb.TenantClusterGrant{
			{ClusterId: "cluster-open", AllowPrivatePullSources: false},
			{ClusterId: "cluster-private", AllowPrivatePullSources: true},
		},
	}
	cases := []struct {
		name    string
		cluster string
		private bool
		want    bool
	}{
		{"granted cluster, public source", "cluster-open", false, true},
		{"granted cluster, private source without permission", "cluster-open", true, false},
		{"granted cluster, private source with permission", "cluster-private", true, true},
		{"granted cluster, public source with permission", "cluster-private", false, true},
		{"ungranted cluster", "cluster-other", false, false},
		{"ungranted cluster, private", "cluster-other", true, false},
		{"empty cluster", "", false, false},
		{"surrounding whitespace is trimmed", "  cluster-open  ", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := LocalTenantAllowsSourceCluster(tenant, tc.cluster, tc.private); got != tc.want {
				t.Fatalf("LocalTenantAllowsSourceCluster(%q, private=%v) = %v, want %v", tc.cluster, tc.private, got, tc.want)
			}
		})
	}
}

// A tenant with no grants at all reaches nothing. Absent authority must not
// read as unrestricted.
func TestLocalTenantAllowsSourceClusterFailsClosedWithoutGrants(t *testing.T) {
	if LocalTenantAllowsSourceCluster(nil, "cluster-a", false) {
		t.Fatal("a nil tenant authority allowed a source cluster")
	}
	if LocalTenantAllowsSourceCluster(&mediaauthoritypb.TenantAuthority{}, "cluster-a", false) {
		t.Fatal("a tenant with no grants allowed a source cluster")
	}
}
