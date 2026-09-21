//go:build schema_verify

package mediaauthority

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func signedRecoveryFixture(t *testing.T, encoded []byte) []byte {
	t.Helper()
	signed := &mediaauthoritypb.SignedAuthorityEnvelope{}
	if err := proto.Unmarshal(encoded, signed); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	signed.Envelope.IssuedAt = timestamppb.New(now.Add(-time.Minute))
	signed.Envelope.RefreshAfter = timestamppb.New(now.Add(time.Hour))
	signed.Envelope.ValidUntil = timestamppb.New(now.Add(23 * time.Hour))
	if signed.Envelope.Kind == mediaauthoritypb.AuthorityKind_AUTHORITY_KIND_TENANT {
		tenant := &mediaauthoritypb.TenantAuthority{}
		if err := proto.Unmarshal(signed.Envelope.Payload, tenant); err != nil {
			t.Fatal(err)
		}
		for _, grant := range tenant.GetEffectiveClusterGrants() {
			grant.ExpiresAt = timestamppb.New(now.Add(48 * time.Hour))
		}
		payload, err := proto.Marshal(tenant)
		if err != nil {
			t.Fatal(err)
		}
		signed.Envelope.Payload, signed.Envelope.PayloadSha256 = payload, testPayloadDigest(payload)
	}
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i)
	}
	signed, err := sharedauthority.Sign(signed.Envelope, ed25519.NewKeyFromSeed(seed))
	if err != nil {
		t.Fatal(err)
	}
	result, err := proto.Marshal(signed)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestMediaAuthorityRecovery_RealPG(t *testing.T) {
	testMediaAuthorityRecovery(t, startMediaAuthorityRealPG(t))
}

func TestMediaAuthorityRecovery_RealYugabyte(t *testing.T) {
	db, ok := dockerpg.OpenSharedYugabyteDatabase(t, "foghorn_authority_recovery")
	if !ok {
		t.Skip("requires the shared Yugabyte fixture: run make verify-foghorn-db")
	}
	schema, err := dbsql.Content.ReadFile("schema/foghorn.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	testMediaAuthorityRecovery(t, db)
}

func testMediaAuthorityRecovery(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	tenant, trust, _ := storeFixture(t, "cell-a")
	object, _ := signedArtifactFixture(t, mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE, 2)
	store, err := NewStore(db, "cell-a", trust)
	if err != nil {
		t.Fatal(err)
	}
	for _, envelope := range [][]byte{object, tenant} {
		if _, err := store.Apply(ctx, signedRecoveryFixture(t, envelope)); err != nil {
			t.Fatal(err)
		}
	}
	if snapshot, err := store.MediaObjectByPlaybackID(ctx, "artifact-playback"); err != nil || snapshot.Freshness != FreshnessValid {
		t.Fatalf("ordinary object-before-tenant delivery: %+v, %v", snapshot, err)
	}
	// A real revival barrier is durable; current inventory confirms the same
	// version without issuing a new envelope or compiling per object.
	if err := foghorndb.New(db).WithholdTenantObjectsAppliedBefore(ctx, foghorndb.WithholdTenantObjectsAppliedBeforeParams{TenantID: "10000000-0000-0000-0000-000000000001"}); err != nil {
		t.Fatal(err)
	}
	if err := store.RaiseStartupFence(ctx); err != nil {
		t.Fatal(err)
	}
	calls := 0
	confirm := func(_ context.Context, request *commodorepb.RequestMediaAuthorityReplayRequest) (*commodorepb.RequestMediaAuthorityReplayResponse, error) {
		calls++
		response := &commodorepb.RequestMediaAuthorityReplayResponse{RecoveryProtocol: sharedauthority.RecoveryProtocol, ObservedAt: timestamppb.Now()}
		if !request.GetInventoryPage() {
			response.InventoryRequired = true
			return response, nil
		}
		if request.GetAcknowledgedBefore() == nil {
			t.Fatal("missing core watermark")
		}
		response.SummaryChecked, response.HeldMatches = true, true
		response.Confirmed = request.Held
		return response, nil
	}
	if err := store.Reconcile(ctx, confirm); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || store.SummaryOwed() {
		t.Fatalf("recovery calls=%d summary still owed=%v", calls, store.SummaryOwed())
	}
	if snapshot, err := store.MediaObjectByPlaybackID(ctx, "artifact-playback"); err != nil || snapshot.Freshness != FreshnessValid || snapshot.Version != 2 {
		t.Fatalf("inventory did not confirm existing version: %+v, %v", snapshot, err)
	}

	t.Run("an acknowledged restart needs no inventory", func(t *testing.T) {
		if err := store.RaiseStartupFence(ctx); err != nil {
			t.Fatal(err)
		}
		calls := 0
		err := store.Reconcile(ctx, func(_ context.Context, request *commodorepb.RequestMediaAuthorityReplayRequest) (*commodorepb.RequestMediaAuthorityReplayResponse, error) {
			calls++
			if request.GetInventoryPage() {
				t.Fatal("ordinary restart walked the catalog")
			}
			return &commodorepb.RequestMediaAuthorityReplayResponse{RecoveryProtocol: sharedauthority.RecoveryProtocol, ObservedAt: timestamppb.Now(), SummaryChecked: true, HeldMatches: true}, nil
		})
		if err != nil || calls != 1 || store.SummaryOwed() {
			t.Fatalf("restart: calls=%d owed=%v err=%v", calls, store.SummaryOwed(), err)
		}
	})

	t.Run("core clock error does not fence an ordinary restart", func(t *testing.T) {
		if err := store.RaiseStartupFence(ctx); err != nil {
			t.Fatal(err)
		}
		err := store.Reconcile(ctx, func(context.Context, *commodorepb.RequestMediaAuthorityReplayRequest) (*commodorepb.RequestMediaAuthorityReplayResponse, error) {
			return nil, status.Error(codes.FailedPrecondition, "synchronize clocks")
		})
		if err == nil || !store.SummaryOwed() || store.fence.holds("media_object", "anything") {
			t.Fatalf("clock failure blocked local authority: %v", err)
		}
		if err := store.Reconcile(ctx, confirm); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("core transport and database outages preserve unfenced local authority", func(t *testing.T) {
		for _, code := range []codes.Code{codes.Unavailable, codes.Internal, codes.DeadlineExceeded, codes.Unimplemented} {
			if err := store.RaiseStartupFence(ctx); err != nil {
				t.Fatal(err)
			}
			if store.fence.holds("media_object", "anything") {
				t.Fatal("ordinary startup waited for core")
			}
			err := store.Reconcile(ctx, func(context.Context, *commodorepb.RequestMediaAuthorityReplayRequest) (*commodorepb.RequestMediaAuthorityReplayResponse, error) {
				return nil, status.Error(code, "core cannot answer")
			})
			if err == nil || !store.SummaryOwed() || store.fence.holds("media_object", "anything") {
				t.Fatalf("core %s error fenced local state or stopped retries: %v", code, err)
			}
		}
	})

	t.Run("confirmation begun before revival does not clear it", func(t *testing.T) {
		if err := store.RaiseStartupFence(ctx); err != nil {
			t.Fatal(err)
		}
		err := store.Reconcile(ctx, func(ctx context.Context, request *commodorepb.RequestMediaAuthorityReplayRequest) (*commodorepb.RequestMediaAuthorityReplayResponse, error) {
			if request.GetInventoryPage() {
				if err := foghorndb.New(db).WithholdTenantObjectsAppliedBefore(ctx, foghorndb.WithholdTenantObjectsAppliedBeforeParams{TenantID: "10000000-0000-0000-0000-000000000001"}); err != nil {
					t.Fatal(err)
				}
				store.fence.requireRecovery()
			}
			return confirm(ctx, request)
		})
		if err != nil || !store.SummaryOwed() {
			t.Fatalf("racing revival lost recovery: %v", err)
		}
		if object, err := store.MediaObjectByPlaybackID(ctx, "artifact-playback"); err != nil || object.Freshness != FreshnessHardExpired {
			t.Fatalf("old proof cleared revival: %+v, %v", object, err)
		}
		if err := store.Reconcile(ctx, confirm); err != nil {
			t.Fatal(err)
		}
		if store.SummaryOwed() {
			t.Fatal("fresh proof did not finish revival recovery")
		}
	})

	t.Run("unsupported core is retried without fencing an ordinary restart", func(t *testing.T) {
		if err := store.RaiseStartupFence(ctx); err != nil {
			t.Fatal(err)
		}
		err := store.Reconcile(ctx, func(context.Context, *commodorepb.RequestMediaAuthorityReplayRequest) (*commodorepb.RequestMediaAuthorityReplayResponse, error) {
			return &commodorepb.RequestMediaAuthorityReplayResponse{}, nil
		})
		if err == nil || !store.SummaryOwed() || store.fence.holds("media_object", "anything") {
			t.Fatalf("unsupported protocol: error=%v owed=%v", err, store.SummaryOwed())
		}
		if err := store.Reconcile(ctx, confirm); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("bounded pages resume without restarting the catalog", func(t *testing.T) {
		// Metadata-only rows suffice here: inventory does not decode envelopes.
		if _, err := db.ExecContext(ctx, `INSERT INTO foghorn.media_authorities
			(authority_kind, authority_id, authority_version, audience_cell_id, signed_envelope, payload, payload_sha256, signer_key_id, issued_at, refresh_after, valid_until)
			SELECT 'media_object', 'page-' || lpad(n::text, 5, '0'), 1, 'cell-a', ''::bytea, ''::bytea, decode(repeat('00',32),'hex'), 'signer-1', NOW(), NOW()+INTERVAL '1 hour', NOW()+INTERVAL '2 hours'
			FROM generate_series(1, 2100) AS n`); err != nil {
			t.Fatal(err)
		}
		if err := store.RaiseStartupFence(ctx); err != nil {
			t.Fatal(err)
		}
		pages, last := 0, ""
		paged := func(ctx context.Context, request *commodorepb.RequestMediaAuthorityReplayRequest) (*commodorepb.RequestMediaAuthorityReplayResponse, error) {
			if request.GetInventoryPage() {
				pages++
				if len(request.Held) > sharedauthority.RecoveryPageSize {
					t.Fatal("unbounded page")
				}
				for _, item := range request.Held {
					key := fenceKey(item.AuthorityKind, item.AuthorityId)
					if key <= last {
						return nil, fmt.Errorf("inventory restarted at %q after %q", key, last)
					}
					last = key
				}
			}
			return confirm(ctx, request)
		}
		if err := store.Reconcile(ctx, paged); err != nil {
			t.Fatal(err)
		}
		if pages != 4 || !store.SummaryOwed() {
			t.Fatalf("first bounded round: pages=%d owed=%v", pages, store.SummaryOwed())
		}
		if err := store.Reconcile(ctx, paged); err != nil {
			t.Fatal(err)
		}
		if pages != 5 || store.SummaryOwed() {
			t.Fatalf("resumed round: pages=%d owed=%v", pages, store.SummaryOwed())
		}
	})
}
