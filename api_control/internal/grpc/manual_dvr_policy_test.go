package grpc

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	foghornclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func manualDVRContext() context.Context {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyUserID, "recording-user")
	return context.WithValue(ctx, ctxkeys.KeyTenantID, "recording-tenant")
}

func manualDVRBillingPolicy() *sharedpb.DVRPolicy {
	return &sharedpb.DVRPolicy{DefaultWindowSeconds: 1800, MaxWindowSeconds: 3600,
		DefaultSegmentDurationSeconds: 6, MaxEntries: 600}
}

func TestManualDVRRequiresKnownBillingPolicyBeforeSourceAccess(t *testing.T) {
	for _, tc := range []struct {
		name       string
		configured bool
		response   *purserpb.GetTenantBillingStatusResponse
		err        error
		want       codes.Code
	}{
		{"missing billing client", false, nil, nil, codes.Unavailable},
		{"billing lookup unavailable", true, nil, status.Error(codes.Unavailable, "billing temporarily unavailable"), codes.Unavailable},
		{"missing window policy", true, &purserpb.GetTenantBillingStatusResponse{BillingModel: "postpaid"}, nil, codes.Unavailable},
		{"empty window policy", true, &purserpb.GetTenantBillingStatusResponse{BillingModel: "postpaid", DvrPolicy: &sharedpb.DVRPolicy{}}, nil, codes.Unavailable},
		{"suspended", true, &purserpb.GetTenantBillingStatusResponse{IsSuspended: true}, nil, codes.PermissionDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sourceReads atomic.Int32
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherFunc(func(string, string) error {
				sourceReads.Add(1)
				return nil
			})))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			// This optional poison query detects any source access. Its expected
			// error also makes an incorrect fail-open path return Unavailable,
			// so checking the status code alone cannot produce a false green.
			mock.ExpectQuery("unexpected source access").WillReturnError(sql.ErrConnDone)
			s := &CommodoreServer{db: db, logger: logrus.New()}
			if tc.configured {
				s.purserClient = startPurserEntitlementFake(t, &purserEntitlementFake{
					billingStatus: func(context.Context, *purserpb.GetTenantBillingStatusRequest) (*purserpb.GetTenantBillingStatusResponse, error) {
						return tc.response, tc.err
					},
				})
			}
			_, err = s.StartDVR(manualDVRContext(), &sharedpb.StartDVRRequest{InternalName: "recording-source", StreamId: proto.String("recording-stream")})
			if status.Code(err) != tc.want || sourceReads.Load() != 0 {
				t.Fatalf("unknown recording policy reached source/creation path: err=%v source_reads=%d", err, sourceReads.Load())
			}
		})
	}
}

type manualDVRStartSpy struct {
	foghornpb.UnimplementedDVRControlServiceServer
	request atomic.Pointer[sharedpb.StartDVRRequest]
}

func (s *manualDVRStartSpy) StartDVR(_ context.Context, req *sharedpb.StartDVRRequest) (*sharedpb.StartDVRResponse, error) {
	s.request.Store(proto.CloneOf(req))
	return nil, status.Error(codes.Unavailable, "test stops before recording creation")
}

func TestManualDVRRequiredReadsCannotFallBackAndStart(t *testing.T) {
	for _, failure := range []string{"chapter database", "chapter disappeared", "retention database", "processing policy", "healthy", "live rewind only", "forever retention"} {
		t.Run(failure, func(t *testing.T) {
			healthy := failure == "healthy" || failure == "live rewind only" || failure == "forever retention"
			s, mock, closeDB := newMockServer(t)
			t.Cleanup(closeDB)
			spy := &manualDVRStartSpy{}
			listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			rpc := grpc.NewServer()
			foghornpb.RegisterDVRControlServiceServer(rpc, spy)
			go func() { _ = rpc.Serve(listener) }()
			t.Cleanup(func() { rpc.Stop(); _ = listener.Close() })
			s.foghornPool = foghornclient.NewPool(foghornclient.PoolConfig{AllowInsecure: true, ServiceToken: "test-token", Timeout: time.Second, Logger: logrus.New()})
			t.Cleanup(func() { _ = s.foghornPool.Close() })
			s.routeCacheTTL = time.Minute
			s.routeCache = map[string]*clusterRoute{"recording-tenant": {
				clusterID: "ingest", resolvedAt: time.Now(),
				clusterPeers: []*clusterpeerpb.TenantClusterPeer{{ClusterId: "ingest", FoghornGrpcAddr: listener.Addr().String()}},
			}}
			var billingCalls atomic.Int32
			fake := &purserEntitlementFake{billingStatus: func(context.Context, *purserpb.GetTenantBillingStatusRequest) (*purserpb.GetTenantBillingStatusResponse, error) {
				billingCalls.Add(1)
				bound := int32(45)
				if failure == "forever retention" {
					bound = 0
				}
				return &purserpb.GetTenantBillingStatusResponse{BillingModel: "postpaid", RecordingRetentionDays: bound, DvrPolicy: manualDVRBillingPolicy()}, nil
			}}
			if failure == "processing policy" {
				fake.subscription = func(context.Context, *purserpb.GetSubscriptionRequest) (*purserpb.GetSubscriptionResponse, error) {
					return nil, status.Error(codes.Unavailable, "connection refused")
				}
			}
			s.purserClient = startPurserEntitlementFake(t, fake)
			mock.ExpectQuery("GetDVRSourceRoute").WithArgs("recording-stream", "recording-tenant").
				WillReturnRows(sqlmock.NewRows([]string{"cluster", "updated"}).AddRow("ingest", time.Now()))
			if failure != "processing policy" {
				retention := mock.ExpectQuery("GetStreamRetentionOverrides").WithArgs("recording-stream", "recording-tenant")
				if failure == "retention database" {
					retention.WillReturnError(errors.New("temporary retention database failure"))
				} else {
					days := 14
					if failure == "forever retention" {
						days = 0
					}
					retention.WillReturnRows(sqlmock.NewRows([]string{"stream_id", "dvr_retention_days_override", "clip_retention_days_override"}).AddRow("recording-stream", days, nil))
					chapter := mock.ExpectQuery("GetStreamDVRChapterConfig").WithArgs("recording-stream", "recording-tenant")
					switch failure {
					case "chapter database":
						chapter.WillReturnError(sql.ErrConnDone)
					case "chapter disappeared":
						chapter.WillReturnError(sql.ErrNoRows)
					case "live rewind only":
						chapter.WillReturnRows(sqlmock.NewRows([]string{"dvr_chapter_mode", "dvr_chapter_interval_seconds"}).AddRow(nil, nil))
					default:
						chapter.WillReturnRows(sqlmock.NewRows([]string{"dvr_chapter_mode", "dvr_chapter_interval_seconds"}).AddRow("fixed_interval", 86400))
					}
				}
			}
			_, err = s.StartDVR(manualDVRContext(), &sharedpb.StartDVRRequest{InternalName: "recording-source", StreamId: proto.String("recording-stream")})
			want := codes.Unavailable
			if failure == "chapter disappeared" {
				want = codes.NotFound
			}
			if status.Code(err) != want {
				t.Fatalf("required policy read returned the wrong error: %v", err)
			}
			if failure == "processing policy" && fake.subscriptionCalls.Load() != 1 {
				t.Fatalf("interactive processing-policy read retried an unavailable owner: calls=%d", fake.subscriptionCalls.Load())
			}
			started := spy.request.Load()
			if !healthy && started != nil {
				t.Fatalf("%s failure dispatched a recording with fallback settings: mode=%q interval=%d retention=%d", failure, started.GetDvrChapterMode(), started.GetDvrChapterIntervalSeconds(), started.GetDvrPolicy().GetRecordingRetentionDays())
			}
			if healthy {
				mode, interval, days := "fixed_interval", int32(86400), int32(14)
				if failure == "live rewind only" {
					mode, interval = "none", 0
				}
				if failure == "forever retention" {
					days = 0
				}
				if started == nil || started.GetDvrChapterMode() != mode || started.GetDvrChapterIntervalSeconds() != interval ||
					started.GetDvrPolicy().GetRecordingRetentionDays() != days || started.GetDvrPolicy().RecordingRetentionDays == nil || billingCalls.Load() != 1 {
					t.Fatalf("manual start lost saved policy or repeated billing lookup: request=%v billing_calls=%d", started, billingCalls.Load())
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestManualDVRRetentionUsesKnownBound(t *testing.T) {
	for _, tc := range []struct {
		name           string
		stream, tenant any
		bound, want    int32
	}{
		{"explicit forever", int32(0), nil, 0, 0},
		{"forever clamped by tier", int32(0), nil, 7, 7},
		{"stream clamped by tier", int32(90), nil, 30, 30},
		{"tenant inheritance", nil, int32(14), 45, 14},
		{"system inheritance", nil, nil, 0, 30},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock, closeDB := newMockServer(t)
			t.Cleanup(closeDB)
			mock.ExpectQuery("GetStreamRetentionOverrides").WithArgs("recording-stream", "recording-tenant").
				WillReturnRows(sqlmock.NewRows([]string{"stream_id", "dvr_retention_days_override", "clip_retention_days_override"}).AddRow("recording-stream", tc.stream, nil))
			if tc.stream == nil {
				row := mock.ExpectQuery("GetTenantMediaRetentionPolicy").WithArgs("recording-tenant")
				if tc.tenant == nil {
					row.WillReturnError(sql.ErrNoRows)
				} else {
					row.WillReturnRows(sqlmock.NewRows([]string{"default_vod_retention_days", "default_dvr_retention_days", "default_clip_retention_days", "updated_by", "updated_at", "keep_vod_originals"}).
						AddRow(nil, tc.tenant, nil, "", time.Now(), nil))
				}
			}
			days, err := s.resolveInitialRetentionWithBound(t.Context(), commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_DVR, "recording-tenant", "recording-stream", tc.bound)
			if err != nil || days != tc.want {
				t.Fatalf("known retention bound changed or defaulted: got=%d want=%d err=%v", days, tc.want, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
