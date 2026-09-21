package mediaauthority

import (
	"context"
	"regexp"
	"slices"
	"testing"

	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/DATA-DOG/go-sqlmock"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
)

func withPlacementEnforced(t *testing.T, enforced bool) {
	t.Helper()
	previous := PlacementEnforced()
	SetPlacementEnforced(enforced)
	t.Cleanup(func() { SetPlacementEnforced(previous) })
}

func TestShadowComparableGatesSchema2PairsOnLocalEnforcement(t *testing.T) {
	legacyTenant := &mediaauthoritypb.TenantAuthority{SchemaVersion: 1}
	legacyObject := &mediaauthoritypb.MediaObjectAuthority{SchemaVersion: 1}
	policyTenant := &mediaauthoritypb.TenantAuthority{SchemaVersion: 2, MediaPlacement: &placementpb.PolicySet{Revision: 3}}
	policyObject := &mediaauthoritypb.MediaObjectAuthority{SchemaVersion: 2, MediaPlacement: &placementpb.PolicySet{}, PlacementTenantRevision: 3}
	staleObject := &mediaauthoritypb.MediaObjectAuthority{SchemaVersion: 2, MediaPlacement: &placementpb.PolicySet{}, PlacementTenantRevision: 2}
	for _, enforced := range []bool{false, true} {
		withPlacementEnforced(t, enforced)
		if !ShadowComparable(legacyTenant, legacyObject) {
			t.Fatalf("enforced=%t: legacy pair stopped being comparable", enforced)
		}
		if ShadowComparable(policyTenant, policyObject) != enforced {
			t.Fatalf("enforced=%t: schema-2 pair comparability did not follow local enforcement", enforced)
		}
		if ShadowComparable(policyTenant, staleObject) || ShadowComparable(policyTenant, legacyObject) || ShadowComparable(legacyTenant, policyObject) {
			t.Fatalf("enforced=%t: stale or mixed pair became comparable", enforced)
		}
	}
}

func TestCellPlacementCapabilityRequiresEveryLiveReplicaAndLocalEnforcement(t *testing.T) {
	for _, test := range []struct {
		name        string
		live        int64
		minSchema   int32
		allEnforced bool
		local       bool
		minFeatures int32
		want        bool
		nodeReady   bool
		features    bool
	}{
		{"no live replicas", 0, 0, false, true, 0, false, false, false},
		{"replica below schema 2", 2, 1, true, true, 1, false, false, true},
		{"replica not enforcing", 2, 2, false, true, 1, false, false, true},
		{"local process not enforcing", 2, 2, true, false, 1, false, false, true},
		{"mixed-release cell withholds node placement", 2, 2, true, true, 0, true, false, false},
		{"every live replica supports node placement", 2, 3, true, true, 1, true, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			withPlacementEnforced(t, test.local)
			store, mock, closeDB := newFixtureStore(t, "cell-a")
			defer closeDB()
			mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*)::bigint AS live_replicas")).WithArgs(int32(ReplicaLivenessWindow.Seconds())).
				WillReturnRows(sqlmock.NewRows([]string{"live_replicas", "min_schema_version", "all_enforced", "min_authority_feature_level"}).
					AddRow(test.live, test.minSchema, test.allEnforced, test.minFeatures))
			capability, err := store.CellPlacementCapability(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			// The listed versions never exceed 2: releases without node
			// placement reject a higher version as a malformed attestation. Long
			// validity and use reporting follow the replicas' feature level alone:
			// they do not depend on placement enforcement.
			if capability.EnforcementReady != test.want || capability.LiveReplicas != test.live ||
				capability.NodePlacementReady != test.nodeReady ||
				capability.LongValidityReady != test.features || capability.UseReportsReady != test.features ||
				!slices.Equal(capability.SupportedSchemaVersions, []uint32{1, 2}) {
				t.Fatalf("capability = %+v, want ready=%t", capability, test.want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
	var missing *Store
	if _, err := missing.CellPlacementCapability(context.Background()); err == nil {
		t.Fatal("nil store attested capability")
	}
}

func TestRecordReplicaHeartbeatWritesCurrentEnforcementState(t *testing.T) {
	for _, enforced := range []bool{false, true} {
		withPlacementEnforced(t, enforced)
		store, mock, closeDB := newFixtureStore(t, "cell-a")
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO foghorn.control_replicas")).
			WithArgs("replica-1", "v0.3.0", int32(sharedauthority.NodePlacementSchemaVersion), enforced, int32(replicaAuthorityFeatureLevel)).WillReturnResult(sqlmock.NewResult(1, 1))
		if err := store.RecordReplicaHeartbeat(context.Background(), " replica-1 ", "v0.3.0"); err != nil {
			t.Fatal(err)
		}
		if err := store.RecordReplicaHeartbeat(context.Background(), " ", "v0.3.0"); err == nil {
			t.Fatal("blank replica identity accepted")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
		closeDB()
	}
}

func TestPromotePlacementReadinessOnlyForSchema2OnEnforcingReplica(t *testing.T) {
	envelope := &mediaauthoritypb.AuthorityEnvelope{AuthorityId: "live:stream", AuthorityVersion: 9}
	for _, test := range []struct {
		name     string
		enforced bool
		schema   uint32
		tenant   bool
		updates  int
	}{
		{"legacy tenant on enforcing replica", true, 1, true, 0},
		{"schema-2 tenant on legacy replica", false, 2, true, 0},
		{"schema-2 tenant on enforcing replica", true, 2, true, 3},
		{"schema-2 object on enforcing replica", true, 2, false, 3},
		{"schema-3 tenant on legacy replica", false, 3, true, 0},
		{"schema-3 tenant on enforcing replica", true, 3, true, 3},
		{"schema-3 object on enforcing replica", true, 3, false, 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			withPlacementEnforced(t, test.enforced)
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			verified := &sharedauthority.Verified{Envelope: envelope}
			if test.tenant {
				verified.Tenant = &mediaauthoritypb.TenantAuthority{SchemaVersion: test.schema, TenantId: "tenant"}
			} else {
				verified.MediaObject = &mediaauthoritypb.MediaObjectAuthority{SchemaVersion: test.schema, TenantId: "tenant"}
			}
			table := "foghorn.media_object_authority_projection"
			if test.tenant {
				table = "foghorn.tenant_authority_projection"
			}
			for range test.updates {
				mock.ExpectExec(regexp.QuoteMeta("UPDATE " + table)).WillReturnResult(sqlmock.NewResult(0, 1))
			}
			if err := promotePlacementReadiness(context.Background(), foghorndb.New(db), verified); err != nil {
				t.Fatal(err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
