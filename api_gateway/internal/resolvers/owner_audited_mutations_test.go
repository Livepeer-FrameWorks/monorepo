package resolvers

import (
	"context"
	"testing"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients/clientstest"
	"frameworks/api_gateway/internal/middleware"
	decklogclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/decklog"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/emptypb"
)

// recordingDecklog is Bridge's Decklog client with every service-event send
// recorded; any other send panics on the nil embedded interface.
type recordingDecklog struct {
	decklogclient.Interface
	sent []*ipcpb.ServiceEvent
}

func (d *recordingDecklog) SendServiceEvent(event *ipcpb.ServiceEvent) error {
	d.sent = append(d.sent, event)
	return nil
}

func (d *recordingDecklog) SendServiceEventContext(_ context.Context, event *ipcpb.ServiceEvent) error {
	d.sent = append(d.sent, event)
	return nil
}

// Clip creation, the upload lifecycle, the Mollie first payment and
// subscription, and cluster subscribe/unsubscribe are recorded by their owning
// services with the caller as actor (clip.requested, upload.*,
// billing.payment_created, billing.subscription_created,
// tenant.cluster_assigned/unassigned). Bridge sends no event of its own for
// them.
func TestOwnerAuditedMutationsSendNoBridgeEvent(t *testing.T) {
	tenantID := uuid.NewString()
	sink := &recordingDecklog{}
	sc := clientstest.Clients(
		clientstest.WithCommodore(&clientstest.FakeCommodore{
			CreateClipFn: func(context.Context, *sharedpb.CreateClipRequest) (*sharedpb.CreateClipResponse, error) {
				return &sharedpb.CreateClipResponse{Status: "queued", ClipHash: "clip-hash", RequestId: "req-1"}, nil
			},
			CreateVodUploadFn: func(context.Context, *sharedpb.CreateVodUploadRequest) (*sharedpb.CreateVodUploadResponse, error) {
				return &sharedpb.CreateVodUploadResponse{UploadId: "upload-1", ArtifactHash: "vod-hash"}, nil
			},
			CompleteVodUploadFn: func(context.Context, *sharedpb.CompleteVodUploadRequest) (*sharedpb.CompleteVodUploadResponse, error) {
				return &sharedpb.CompleteVodUploadResponse{Asset: &sharedpb.VodAssetInfo{ArtifactHash: "vod-hash"}}, nil
			},
			AbortVodUploadFn: func(context.Context, string, string) (*sharedpb.AbortVodUploadResponse, error) {
				return &sharedpb.AbortVodUploadResponse{Success: true}, nil
			},
		}),
		clientstest.WithPurser(&clientstest.FakePurser{
			CreateMollieFirstPaymentFn: func(context.Context, string, string, string, string) (*purserpb.CreateMollieFirstPaymentResponse, error) {
				return &purserpb.CreateMollieFirstPaymentResponse{PaymentId: "tr_1", PaymentUrl: "https://pay.example"}, nil
			},
			CreateMollieSubscriptionFn: func(context.Context, string, string, string, string) (*purserpb.CreateMollieSubscriptionResponse, error) {
				return &purserpb.CreateMollieSubscriptionResponse{SubscriptionId: "sub_1", Status: "active"}, nil
			},
			CreateClusterSubscriptionFn: func(context.Context, string, string, string) (*purserpb.ClusterSubscriptionResponse, error) {
				return &purserpb.ClusterSubscriptionResponse{Status: "active"}, nil
			},
		}),
		clientstest.WithQuartermaster(&clientstest.FakeQuartermaster{
			UnsubscribeFromClusterFn: func(context.Context, *quartermasterpb.UnsubscribeFromClusterRequest) (*emptypb.Empty, error) {
				return &emptypb.Empty{}, nil
			},
		}),
	)
	sc.Decklog = sink
	r := &Resolver{Clients: sc, Logger: clientstest.DiscardLogger()}
	ctx := context.WithValue(clientstest.AuthedCtx(tenantID), ctxkeys.KeyUser, &middleware.UserContext{UserID: "user-1", TenantID: tenantID, Role: "owner"})

	for name, run := range map[string]func() error{
		"createClip": func() error {
			_, err := r.DoCreateClip(ctx, model.CreateClipInput{StreamID: uuid.NewString(), Title: "clip"})
			return err
		},
		"createVodUpload": func() error {
			_, err := r.DoCreateVodUpload(ctx, model.CreateVodUploadInput{Filename: "video.mp4", SizeBytes: 1024})
			return err
		},
		"completeVodUpload": func() error {
			_, err := r.DoCompleteVodUpload(ctx, model.CompleteVodUploadInput{UploadID: "upload-1"})
			return err
		},
		"abortVodUpload": func() error {
			_, err := r.DoAbortVodUpload(ctx, "upload-1")
			return err
		},
		"createMollieFirstPayment": func() error {
			_, err := r.DoCreateMollieFirstPayment(ctx, "tier-1", "ideal", "https://return.example")
			return err
		},
		"createMollieSubscription": func() error {
			_, err := r.DoCreateMollieSubscription(ctx, "tier-1", "mdt_1", nil)
			return err
		},
		"subscribeToCluster": func() error {
			_, err := r.DoCreateClusterSubscription(ctx, "cluster-1")
			return err
		},
		"unsubscribeFromCluster": func() error {
			_, err := r.DoUnsubscribeFromCluster(ctx, "cluster-1")
			return err
		},
	} {
		if err := run(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(sink.sent) != 0 {
			t.Fatalf("%s sent Bridge events %v, want none", name, sink.sent)
		}
	}
}
