//go:build schema_verify

package grpc

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"github.com/lib/pq"
	"google.golang.org/protobuf/proto"
)

func seedCommercialTenantVersion(ctx context.Context, db *sql.DB, tenant *mediapb.TenantAuthority, version int64) error {
	encoded, err := proto.Marshal(tenant)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(encoded)
	now := time.Now().UTC()
	q := commodoredb.New(db)
	if err := q.InsertMediaAuthorityVersion(ctx, commodoredb.InsertMediaAuthorityVersionParams{AuthorityKind: "tenant", AuthorityID: tenant.TenantId, AuthorityVersion: version, PayloadSchemaVersion: 2, Payload: encoded, PayloadSha256: digest[:], SourceRevisions: []byte("[]"), IssuedAt: now, RefreshAfter: now.Add(30 * time.Second), ValidUntil: now.Add(time.Minute)}); err != nil {
		return err
	}
	if _, err := q.UpsertCurrentMediaAuthority(ctx, commodoredb.UpsertCurrentMediaAuthorityParams{AuthorityKind: "tenant", AuthorityID: tenant.TenantId, AuthorityVersion: version}); err != nil {
		return err
	}
	if err := q.UpsertMediaAuthorityTarget(ctx, commodoredb.UpsertMediaAuthorityTargetParams{AuthorityKind: "tenant", AuthorityID: tenant.TenantId, AuthorityVersion: version, CellID: "cell-a"}); err != nil {
		return err
	}
	_, err = q.EnqueueMediaAuthorityDelivery(ctx, commodoredb.EnqueueMediaAuthorityDeliveryParams{AuthorityKind: "tenant", AuthorityID: tenant.TenantId, AuthorityVersion: version, CellID: "cell-a", SignedEnvelope: []byte{1}})
	return err
}

func TestMediaPlacementCommercialPublication_RealPG(t *testing.T) {
	t.Run("quoted", func(t *testing.T) { testMediaPlacementCommercialPublication(t, true) })
	t.Run("noncommercial", func(t *testing.T) { testMediaPlacementCommercialPublication(t, false) })
}

func testMediaPlacementCommercialPublication(t *testing.T, quoted bool) {
	db := startCommodoreRealPG(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tenant, baseObject := quotedCommercialAuthorityFixture()
	if !quoted {
		tenant.MediaPlacement.Ingest, tenant.MediaPlacement.Serve = nil, nil
	}
	if err := seedCommercialTenantVersion(ctx, db, tenant, 1); err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, changed := range []bool{false, true} {
		if changed && !quoted {
			continue
		}
		object := proto.CloneOf(baseObject)
		if changed {
			object.GetLiveStream().StreamId = "changed-stream"
		}
		var change sync.Once
		source := commercialSourceFunc(func(callCtx context.Context, request *pb.CommercialQuoteRequest) (*pb.CommercialQuoteResponse, error) {
			var writeErr error
			if changed {
				change.Do(func() {
					replacement := proto.CloneOf(tenant)
					replacement.EffectiveClusterGrants[0].MediaConsent.Revision++
					writeErr = seedCommercialTenantVersion(callCtx, db, replacement, 2)
				})
			}
			if writeErr != nil {
				return nil, writeErr
			}
			return commercialResponse(tenant, request)
		})
		server := &CommodoreServer{db: db, authorityCommercialSource: source, mediaAuthorityKeyID: "commercial-key", mediaAuthorityPrivateKey: private}
		if !quoted {
			server.authorityCommercialSource = nil
		}
		id := sharedauthority.LiveStreamAuthorityID(object.GetLiveStream().GetStreamId())
		publishErr := server.persistMediaObjectAuthority(ctx, id, object, []string{"cell-a"}, []*mediapb.AuthoritySourceRevision{{Service: "commodore", Revision: "stream-source"}}, time.Now(), time.Now().Add(time.Minute))
		if (publishErr != nil) != changed {
			t.Fatalf("publication changed=%v: %v", changed, publishErr)
		}
		current, readErr := commodoredb.New(db).GetCurrentMediaAuthorityPayload(ctx, commodoredb.GetCurrentMediaAuthorityPayloadParams{AuthorityKind: "media_object", AuthorityID: id})
		if changed {
			if !errors.Is(readErr, sql.ErrNoRows) {
				t.Fatalf("mixed parent published object: %v", readErr)
			}
			continue
		}
		if readErr != nil {
			t.Fatal(readErr)
		}
		payload := &mediapb.MediaObjectAuthority{}
		if err := proto.Unmarshal(current.Payload, payload); err != nil {
			t.Fatal(err)
		}
		wantQuotes := 0
		if quoted {
			wantQuotes = 2
		}
		if len(payload.CommercialQuotes) != wantQuotes {
			t.Fatal("stored authority has incorrect quote selection")
		}
		for _, quote := range payload.CommercialQuotes {
			if current.ValidUntil.After(quote.ExpiresAt.AsTime()) {
				t.Fatal("stored authority exceeded quote lease")
			}
		}
		if !quoted && !current.ValidUntil.After(time.Now().Add(45*time.Second)) {
			t.Fatal("noncommercial authority was constrained by absent billing quotes")
		}
		var signedBytes []byte
		if err := db.QueryRowContext(ctx, "SELECT signed_envelope FROM commodore.media_authority_deliveries WHERE authority_kind='media_object' AND authority_id=$1 AND cell_id='cell-a'", id).Scan(&signedBytes); err != nil {
			t.Fatal(err)
		}
		signed := &mediapb.SignedAuthorityEnvelope{}
		if err := proto.Unmarshal(signedBytes, signed); err != nil {
			t.Fatal(err)
		}
		verified, err := sharedauthority.Verify(signed, sharedauthority.TrustSet{"commercial-key": public}, "cell-a", time.Now())
		if err != nil || !proto.Equal(verified.MediaObject, payload) {
			t.Fatalf("delivery did not preserve signed commercial payload: %v", err)
		}
		if !quoted {
			for _, revision := range signed.Envelope.SourceRevisions {
				if revision.Service == "purser" {
					t.Fatal("unquoted publication claimed billing provenance")
				}
			}
		}
		if !signed.Envelope.RefreshAfter.AsTime().After(signed.Envelope.IssuedAt.AsTime()) || !signed.Envelope.RefreshAfter.AsTime().Before(current.ValidUntil) {
			t.Fatal("quote renewal has no headroom")
		}
	}
}

func TestMediaPlacementCommercialParentLock_RealPG(t *testing.T) {
	db := startCommodoreRealPG(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tenant, _ := commercialAuthorityFixture()
	if err := seedCommercialTenantVersion(ctx, db, tenant, 1); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := commodoredb.New(tx).LockCurrentTenantMediaAuthority(ctx, tenant.TenantId); err != nil {
		t.Fatal(err)
	}
	if _, err := commodoredb.New(tx).LockCurrentTenantMediaAuthority(ctx, "81000000-0000-4000-8000-000000000002"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-tenant lock read = %v", err)
	}
	writer, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback()
	if _, err := writer.ExecContext(ctx, "SET LOCAL lock_timeout = '200ms'"); err != nil {
		t.Fatal(err)
	}
	_, blocked := writer.ExecContext(ctx, "UPDATE commodore.media_authority_current SET updated_at=NOW() WHERE authority_kind='tenant' AND authority_id=$1", tenant.TenantId)
	var lockError *pq.Error
	if !errors.As(blocked, &lockError) || lockError.Code != "55P03" {
		t.Fatalf("expected parent row lock conflict, got %v", blocked)
	}
	if err := writer.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE commodore.media_authority_current SET updated_at=NOW() WHERE authority_kind='tenant' AND authority_id=$1", tenant.TenantId); err != nil {
		t.Fatalf("parent remained locked after publication: %v", err)
	}
}
