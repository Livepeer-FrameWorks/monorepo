package grpc

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	clusterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type commercialSourceFunc func(context.Context, *pb.CommercialQuoteRequest) (*pb.CommercialQuoteResponse, error)

func (fn commercialSourceFunc) GetMediaPlacementQuote(ctx context.Context, request *pb.CommercialQuoteRequest) (*pb.CommercialQuoteResponse, error) {
	return fn(ctx, request)
}

func commercialAuthorityFixture() (*mediapb.TenantAuthority, *mediapb.MediaObjectAuthority) {
	const tenantID = "81000000-0000-4000-8000-000000000001"
	tenant := &mediapb.TenantAuthority{SchemaVersion: 2, TenantId: tenantID, Lifecycle: mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE, BillingDecision: mediapb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW, MediaPlacement: &pb.PolicySet{Revision: 3}, EffectiveClusterGrants: []*mediapb.TenantClusterGrant{{ClusterId: "official", ControlCellId: "cell-a", SubscriptionStatus: "active", ClusterClass: "platform_official", AccessSource: clusterpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PLATFORM_TIER, MediaConsent: &pb.CapacityConsent{AllowIngest: true, AllowServe: true, AllowExternalSource: true}}}}
	object := &mediapb.MediaObjectAuthority{SchemaVersion: 2, TenantId: tenantID, InternalName: "internal", PlaybackId: "playback", Lifecycle: mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE, ObjectKind: mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM, MediaPlacement: &pb.PolicySet{Revision: 5}, PlacementTenantRevision: 3, PlaybackPolicy: &mediapb.PlaybackPolicy{Kind: mediapb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_PUBLIC}, Object: &mediapb.MediaObjectAuthority_LiveStream{LiveStream: &mediapb.LiveStreamAuthority{StreamId: "stream", IngestMode: "push"}}}
	return tenant, object
}

func quotedCommercialAuthorityFixture() (*mediapb.TenantAuthority, *mediapb.MediaObjectAuthority) {
	tenant, object := commercialAuthorityFixture()
	rules := &pb.Rules{SchemaVersion: 1, Constraints: &pb.Constraints{Allow: &pb.SelectorSet{Any: []*pb.Selector{{Charging: []pb.Charging{pb.Charging_CHARGING_RATED}}}}}}
	tenant.MediaPlacement.Ingest, tenant.MediaPlacement.Serve = proto.CloneOf(rules), proto.CloneOf(rules)
	return tenant, object
}

func commercialResponse(tenant *mediapb.TenantAuthority, request *pb.CommercialQuoteRequest) (*pb.CommercialQuoteResponse, error) {
	scope, digest, err := placement.CanonicalCommercialQuoteRequest(request)
	if err != nil {
		return nil, err
	}
	entitlement, err := sharedauthority.PlacementCommercialEntitlementDigest(tenant)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	response := &pb.CommercialQuoteResponse{Scope: scope, RequestDigest: digest, EntitlementDigest: entitlement, ObservedAt: timestamppb.New(now), ExpiresAt: timestamppb.New(now.Add(30 * time.Second)), Usage: &pb.QuoteUsageEvidence{Status: pb.QuoteUsageStatus_QUOTE_USAGE_STATUS_NOT_REQUIRED}}
	for _, id := range scope.ClusterIds {
		response.Clusters = append(response.Clusters, &pb.ClusterCommercialQuote{ClusterId: id, Facts: &pb.CommercialFacts{Charging: pb.Charging_CHARGING_RATED, Revision: strings.Repeat("b", 64), ExpiresAt: proto.CloneOf(response.ExpiresAt)}})
	}
	return response, nil
}

func TestCollectMediaObjectCommercialConcurrentCompleteAndDetached(t *testing.T) {
	tenant, object := quotedCommercialAuthorityFixture()
	before := proto.CloneOf(object)
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var calls atomic.Int32
	source := commercialSourceFunc(func(ctx context.Context, request *pb.CommercialQuoteRequest) (*pb.CommercialQuoteResponse, error) {
		calls.Add(1)
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 5*time.Second {
			return nil, errors.New("missing quote timeout")
		}
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return commercialResponse(tenant, request)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() {
		for range 2 {
			select {
			case <-entered:
			case <-ctx.Done():
				return
			}
		}
		close(release)
	}()
	result, err := collectMediaObjectCommercial(ctx, source, tenant, object, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || len(result.payload.CommercialQuotes) != 2 || result.payload.CommercialQuotes[0].Scope.Verb != pb.Verb_VERB_INGEST || result.payload.CommercialQuotes[1].Scope.Verb != pb.Verb_VERB_SERVE || !proto.Equal(before, object) {
		t.Fatal("partial, unordered or mutating commercial compilation")
	}
	if len(result.revision) != 64 || result.validUntil.After(time.Now().Add(30*time.Second)) {
		t.Fatal("commercial expiry or source revision lost")
	}
	result.tenant.MediaPlacement.Revision++
	if tenant.MediaPlacement.Revision != 3 {
		t.Fatal("captured tenant aliases caller")
	}
}

func TestCollectMediaObjectCommercialRejectsPartialOrReboundQuotes(t *testing.T) {
	for _, scenario := range []string{"dependency", "wrong object", "changed consent", "expired", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			tenant, object := quotedCommercialAuthorityFixture()
			before := proto.CloneOf(object)
			source := commercialSourceFunc(func(_ context.Context, request *pb.CommercialQuoteRequest) (*pb.CommercialQuoteResponse, error) {
				if request.Verb == pb.Verb_VERB_INGEST && scenario == "dependency" {
					return nil, errors.New("quote unavailable")
				}
				response, err := commercialResponse(tenant, request)
				if err != nil {
					return nil, err
				}
				if request.Verb == pb.Verb_VERB_SERVE {
					switch scenario {
					case "wrong object":
						response.Scope.ObjectId = "live_stream:other"
					case "changed consent":
						response.EntitlementDigest = strings.Repeat("c", 64)
					case "expired":
						response.ExpiresAt = timestamppb.New(time.Now().Add(-time.Second))
					}
				}
				return response, nil
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if scenario == "canceled" {
				cancel()
			}
			result, err := collectMediaObjectCommercial(ctx, source, tenant, object, time.Now().Add(time.Minute))
			if err == nil || result != nil || !proto.Equal(before, object) {
				t.Fatal("partial or invalid quote escaped compilation")
			}
		})
	}
}

func TestPersistMediaObjectCommercialRechecksParentAfterQuoteReads(t *testing.T) {
	for _, test := range []struct {
		name    string
		changed bool
		quoted  bool
	}{{"quoted unchanged", false, true}, {"quoted changed", true, true}, {"unquoted unchanged", false, false}, {"unquoted changed", true, false}} {
		t.Run(test.name, func(t *testing.T) {
			changed := test.changed
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			tenant, object := quotedCommercialAuthorityFixture()
			if !test.quoted {
				tenant.MediaPlacement.Ingest, tenant.MediaPlacement.Serve = nil, nil
			}
			encoded, err := proto.Marshal(tenant)
			if err != nil {
				t.Fatal(err)
			}
			until := time.Now().Add(time.Minute)
			mock.ExpectQuery("GetCurrentMediaAuthorityPayload").WithArgs("tenant", tenant.TenantId).WillReturnRows(sqlmock.NewRows([]string{"payload", "valid_until"}).AddRow(encoded, until))
			mock.ExpectQuery("ListCurrentMediaAuthorityDeliveryCells").WithArgs("tenant", tenant.TenantId).WillReturnRows(sqlmock.NewRows([]string{"cell_id"}).AddRow("cell-a"))
			var calls atomic.Int32
			source := commercialSourceFunc(func(_ context.Context, request *pb.CommercialQuoteRequest) (*pb.CommercialQuoteResponse, error) {
				calls.Add(1)
				if db.Stats().InUse != 0 {
					return nil, errors.New("quote requested while holding database connection")
				}
				return commercialResponse(tenant, request)
			})
			mock.ExpectBegin()
			locked := proto.CloneOf(tenant)
			if changed {
				locked.MediaPlacement.Revision++
			}
			lockedBytes, err := proto.Marshal(locked)
			if err != nil {
				t.Fatal(err)
			}
			mock.ExpectQuery("LockCurrentTenantMediaAuthority").WithArgs(tenant.TenantId).WillReturnRows(sqlmock.NewRows([]string{"payload", "valid_until"}).AddRow(lockedBytes, until))
			if changed {
				mock.ExpectRollback()
			} else {
				mock.ExpectQuery("ListMediaAuthorityPriorCells").WithArgs("media_object", "live_stream:stream").WillReturnRows(sqlmock.NewRows([]string{"cell_id"}))
				mock.ExpectQuery("AllocateMediaAuthorityVersion").WithArgs("media_object", "live_stream:stream").WillReturnRows(sqlmock.NewRows([]string{"last_version"}).AddRow(int64(1)))
				mock.ExpectExec("InsertMediaAuthorityVersion").WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec("UpsertCurrentMediaAuthority").WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec("SupersedeOlderMediaAuthorityDeliveries").WillReturnResult(sqlmock.NewResult(0, 0))
				mock.ExpectExec("UpsertMediaAuthorityTarget").WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec("EnqueueMediaAuthorityDelivery").WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec("ScheduleMediaAuthorityRefresh").WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit()
			}
			_, key, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			server := &CommodoreServer{db: db, authorityCommercialSource: source, mediaAuthorityKeyID: "key", mediaAuthorityPrivateKey: key}
			err = server.persistMediaObjectAuthority(context.Background(), "live_stream:stream", object, []string{"cell-a"}, []*mediapb.AuthoritySourceRevision{{Service: "commodore", Revision: "source"}}, time.Now(), until)
			if (err != nil) != changed {
				t.Fatalf("persistence error = %v, changed=%v", err, changed)
			}
			wantCalls := int32(0)
			if test.quoted {
				wantCalls = 2
			}
			if calls.Load() != wantCalls || len(object.CommercialQuotes) != 0 {
				t.Fatal("incorrect quote calls or mutated caller object")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCommercialAuthorityRefreshKeepsRenewalHeadroom(t *testing.T) {
	now := time.Now()
	if got := mediaAuthorityRefreshAfter(now, now.Add(30*time.Second)); !got.Equal(now.Add(15 * time.Second)) {
		t.Fatalf("short lease refresh = %v", got)
	}
	if got := mediaAuthorityRefreshAfter(now, now.Add(time.Hour)); !got.Equal(now.Add(mediaAuthorityRefreshInterval)) {
		t.Fatalf("long lease refresh = %v", got)
	}
}

func TestCollectMediaObjectCommercialArtifactRefreshesOnlyServe(t *testing.T) {
	tenant, object := quotedCommercialAuthorityFixture()
	object.ObjectKind = mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_ARTIFACT
	object.Object = &mediapb.MediaObjectAuthority_Artifact{Artifact: &mediapb.ArtifactAuthority{ArtifactId: "artifact"}}
	stale := &pb.CommercialQuoteResponse{RequestDigest: "stale"}
	object.CommercialQuotes = []*pb.CommercialQuoteResponse{stale}
	calls := 0
	source := commercialSourceFunc(func(_ context.Context, request *pb.CommercialQuoteRequest) (*pb.CommercialQuoteResponse, error) {
		calls++
		if request.Verb != pb.Verb_VERB_SERVE || request.ObjectId != sharedauthority.ArtifactAuthorityID("artifact") {
			return nil, errors.New("artifact requested non-serving commercial scope")
		}
		return commercialResponse(tenant, request)
	})
	result, err := collectMediaObjectCommercial(context.Background(), source, tenant, object, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(result.payload.CommercialQuotes) != 1 || result.payload.CommercialQuotes[0].RequestDigest == "stale" || !proto.Equal(object.CommercialQuotes[0], stale) {
		t.Fatal("artifact quote was reused or caller evidence changed")
	}
}
