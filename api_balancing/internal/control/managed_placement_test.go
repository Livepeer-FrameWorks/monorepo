package control

import (
	"context"
	"errors"
	"testing"
	"time"

	localauthority "frameworks/api_balancing/internal/mediaauthority"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
)

type managedPlacementFixture struct {
	pair          localauthority.PlacementPair
	secret        *mediapb.LiveStreamSecret
	err           error
	reads, opened int
	t             *testing.T
}

func (f *managedPlacementFixture) Placement(ctx context.Context, tenant, object, internal string) (localauthority.PlacementPair, error) {
	f.reads++
	if tenant != "tenant" || object != "live_stream:stream" || internal != "internal" {
		f.t.Fatal("managed lookup lost exact tenant/object/name scope")
	}
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > time.Second {
		f.t.Fatal("managed lookup is unbounded")
	}
	return f.pair, f.err
}

func (f *managedPlacementFixture) OpenLiveStreamSecret(snapshot localauthority.MediaObjectSnapshot) (*mediapb.LiveStreamSecret, error) {
	f.opened++
	if snapshot.AuthorityID != f.pair.Object.AuthorityID || snapshot.Version != f.pair.Object.Version {
		f.t.Fatal("source secret did not use joined object version")
	}
	return f.secret, nil
}

func TestManagedPlacementChecksSignedConstraintsAndSource(t *testing.T) {
	for _, scenario := range []string{"allow", "short tenant lease", "deny", "no consent", "billing denied", "object revoked", "foreign context", "source changed", "wrong source cluster", "multiple source clusters", "not source ready", "expired", "mixed parent", "lookup error", "canceled", "schema one", "preference elsewhere", "empty preferences", "elected node denied", "other node denied"} {
		t.Run(scenario, func(t *testing.T) {
			now := time.Now()
			f := &managedPlacementFixture{t: t, secret: &mediapb.LiveStreamSecret{NativeSourceSpec: "file:/media/input.ts", NativeSourceKind: "file", NativeAllowedClusterIds: []string{"source"}, NativePlacementCount: 1}}
			f.pair = localauthority.PlacementPair{
				Tenant: localauthority.TenantSnapshot{Version: 1, IngestReady: true, SourceReady: true, ValidUntil: now.Add(time.Minute), Authority: &mediapb.TenantAuthority{
					SchemaVersion: 2, TenantId: "tenant", Lifecycle: mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE, BillingDecision: mediapb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW,
					MediaPlacement: &pb.PolicySet{Revision: 1}, EffectiveClusterGrants: []*mediapb.TenantClusterGrant{{ClusterId: "source", ControlCellId: "cell", ClusterClass: "tenant_private", OwnerTenantId: "tenant", SubscriptionStatus: "active", MediaConsent: &pb.CapacityConsent{AllowIngest: true}}},
				}},
				Object: localauthority.MediaObjectSnapshot{AuthorityID: "live_stream:stream", Version: 1, IngestReady: true, SourceReady: true, ValidUntil: now.Add(time.Minute), Authority: &mediapb.MediaObjectAuthority{
					SchemaVersion: 2, TenantId: "tenant", InternalName: "internal", Lifecycle: mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
					ObjectKind: mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM, MediaPlacement: &pb.PolicySet{}, PlacementTenantRevision: 1,
					Object: &mediapb.MediaObjectAuthority_LiveStream{LiveStream: &mediapb.LiveStreamAuthority{StreamId: "stream", IngestMode: "mist_native"}},
				}},
			}
			row := &commodorepb.ManagedStreamRow{TenantId: "tenant", StreamId: "stream", InternalName: "internal", IngestMode: "mist_native", SourceSpec: f.secret.NativeSourceSpec, SourceKind: "file", AllowedClusterIds: []string{"source"}, PlacementCount: 1}
			streamCtx := &commodorepb.ResolveStreamContextResponse{Admitted: true, TenantId: "tenant", StreamId: "stream", InternalName: "internal", IngestMode: "mist_native"}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := materializeTransient
			switch scenario {
			case "allow":
				want = materializeOK
			case "short tenant lease":
				f.pair.Tenant.ValidUntil = now.Add(5 * time.Second)
				want = materializeOK
			case "deny":
				f.pair.Tenant.Authority.MediaPlacement.Ingest = &pb.Rules{SchemaVersion: 1, Constraints: &pb.Constraints{Deny: []*pb.Selector{{ClusterIds: []string{"source"}}}}}
				want = materializeDenied
			case "no consent":
				f.pair.Tenant.Authority.EffectiveClusterGrants[0].MediaConsent.AllowIngest = false
				want = materializeDenied
			case "billing denied":
				f.pair.Tenant.Authority.BillingDecision = mediapb.TenantBillingDecision_TENANT_BILLING_DECISION_SUSPENDED
				want = materializeDenied
			case "object revoked":
				f.pair.Object.Authority.Lifecycle = mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_INACTIVE
				want = materializeDenied
			case "foreign context":
				streamCtx.TenantId = "another"
			case "source changed":
				row.SourceSpec = "file:/another.ts"
			case "wrong source cluster":
				f.secret.NativeAllowedClusterIds = []string{"another"}
				want = materializeDenied
			case "multiple source clusters":
				f.secret.NativeAllowedClusterIds = []string{"source", "another"}
			case "not source ready":
				f.pair.Object.SourceReady = false
			case "expired":
				f.pair.Object.ValidUntil = now
			case "mixed parent":
				f.pair.Object.Authority.PlacementTenantRevision = 2
			case "lookup error":
				f.err = errors.New("database unavailable")
			case "canceled":
				cancel()
			case "schema one":
				f.pair.Tenant.Authority.SchemaVersion, f.pair.Object.Authority.SchemaVersion = 1, 1
				f.pair.Tenant.Authority.MediaPlacement, f.pair.Object.Authority.MediaPlacement = nil, nil
				want = materializeOK
			case "preference elsewhere":
				f.pair.Tenant.Authority.MediaPlacement.Ingest = &pb.Rules{SchemaVersion: 1, Preferences: &pb.Preferences{Groups: []*pb.Group{{Id: "elsewhere", Match: &pb.Selector{ClusterIds: []string{"another"}}}}}}
				want = materializeOK
			case "empty preferences":
				f.pair.Tenant.Authority.MediaPlacement.Ingest = &pb.Rules{SchemaVersion: 1, Preferences: &pb.Preferences{}}
				want = materializeDenied
			case "elected node denied":
				f.pair.Tenant.Authority.SchemaVersion, f.pair.Object.Authority.SchemaVersion = 3, 3
				f.pair.Tenant.Authority.MediaPlacement.Ingest = &pb.Rules{SchemaVersion: 1, Constraints: &pb.Constraints{Deny: []*pb.Selector{{NodeIds: []string{"node-1"}}}}}
				want = materializeDenied
			case "other node denied":
				f.pair.Tenant.Authority.SchemaVersion, f.pair.Object.Authority.SchemaVersion = 3, 3
				f.pair.Tenant.Authority.MediaPlacement.Ingest = &pb.Rules{SchemaVersion: 1, Constraints: &pb.Constraints{Deny: []*pb.Selector{{NodeIds: []string{"node-2"}}}}}
				want = materializeOK
			}
			var admission *ipcpb.ManagedStreamAdmission
			if got := checkManagedPlacementAdmission(ctx, f, "source", "node-1", row, streamCtx, &admission); got != want {
				t.Fatalf("managed admission = %v, want %v", got, want)
			}
			if want == materializeOK && scenario != "schema one" {
				if admission == nil || admission.GetTargetClusterId() != "source" || admission.GetObjectAuthorityVersion() != f.pair.Object.Version || admission.GetTenantAuthorityVersion() != f.pair.Tenant.Version ||
					admission.GetPolicyDigest() == "" || !time.Now().Before(admission.GetExpiresAt().AsTime()) || admission.GetExpiresAt().AsTime().After(f.pair.Object.ValidUntil) ||
					admission.GetExpiresAt().AsTime().After(f.pair.Tenant.ValidUntil) ||
					admission.GetExpiresAt().AsTime().Sub(admission.GetIssuedAt().AsTime()) > 30*time.Second {
					t.Fatalf("managed command authority is unbound: %+v", admission)
				}
			} else if admission != nil {
				t.Fatal("failed or legacy check minted schema-2 command authority")
			}
			if scenario == "canceled" && f.reads != 0 {
				t.Fatal("canceled admission read authority")
			}
			if (scenario == "deny" || scenario == "no consent" || scenario == "billing denied" || scenario == "object revoked" || scenario == "foreign context" || scenario == "not source ready" || scenario == "expired" || scenario == "mixed parent" || scenario == "lookup error") && f.opened != 0 {
				t.Fatal("invalid placement opened source secret")
			}
		})
	}
}
