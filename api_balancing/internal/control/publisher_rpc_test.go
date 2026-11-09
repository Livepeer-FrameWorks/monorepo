package control

import (
	"bytes"
	"context"
	"net"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type publisherProcessorFunc func(context.Context, *ipcpb.MistTrigger) (string, bool, error)

func (f publisherProcessorFunc) ProcessPublisher(ctx context.Context, t *ipcpb.MistTrigger) (string, bool, error) {
	return f(ctx, t)
}
func (f publisherProcessorFunc) PublisherAdmissionEnabled() bool { return true }
func (f publisherProcessorFunc) ProcessTypedTrigger(t *ipcpb.MistTrigger) (string, bool, error) {
	return f(context.Background(), t)
}
func (f publisherProcessorFunc) ProcessTrigger(string, []byte, string) (string, bool, error) {
	panic("typed trigger required")
}

func TestPublisherRPCBindsNodeAndCallerDeadline(t *testing.T) {
	previous := mistTriggerProcessor
	t.Cleanup(func() { mistTriggerProcessor = previous })
	stream := &captureStream{}
	t.Cleanup(SetupTestRegistry("canonical", stream))
	c := registry.conns["canonical"]
	c.canonicalID = "canonical"
	c.clusterID = "cluster"
	c.fence = 7
	c.admissionToken = bytes.Repeat([]byte{1}, 32)
	deadline := time.Now().Add(time.Second)
	ctx, cancel := context.WithDeadline(t.Context(), deadline)
	defer cancel()
	req := &ipcpb.AdmitPublisherRequest{NodeId: "canonical", SessionToken: bytes.Clone(c.admissionToken), Trigger: &ipcpb.MistTrigger{TriggerType: "PUSH_REWRITE", NodeId: "forged", TenantId: proto.String("forged"), ClusterId: proto.String("forged"), TriggerPayload: &ipcpb.MistTrigger_PushRewrite{PushRewrite: &ipcpb.PushRewriteTrigger{Pid: 12, TriggerUuid: "exec"}}}}
	called := 0
	mistTriggerProcessor = publisherProcessorFunc(func(callCtx context.Context, trigger *ipcpb.MistTrigger) (string, bool, error) {
		called++
		got, ok := callCtx.Deadline()
		if !ok || !got.Equal(deadline) {
			t.Fatal("caller deadline was reset")
		}
		if trigger.GetNodeId() != "canonical" || trigger.GetClusterId() != "cluster" || trigger.GetTenantId() != "" {
			t.Fatalf("payload identity trusted: %v", trigger)
		}
		trigger.EventId = "generation"
		return "live+stream", false, nil
	})
	server := &Server{}
	forged := proto.CloneOf(req)
	forged.SessionToken[0] ^= 1
	if _, err := server.AdmitPublisher(ctx, forged); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("forged node session allowed: %v", err)
	}
	if _, err := server.AdmitPublisher(t.Context(), req); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("unbounded request allowed: %v", err)
	}
	response, err := server.AdmitPublisher(ctx, req)
	if err != nil || response.GetIngestGeneration() != "generation" || response.GetAbort() || called != 1 {
		t.Fatalf("admission: %+v %v calls=%d", response, err, called)
	}
	if req.GetTrigger().GetNodeId() != "forged" {
		t.Fatal("RPC mutated caller-owned request")
	}
	c.superseded.Store(true)
	if _, err := server.AdmitPublisher(ctx, req); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("retired session allowed: %v", err)
	}
}

func TestPublisherRPCQueueCancellationAndReplacement(t *testing.T) {
	previous, previousSlots := mistTriggerProcessor, publisherAdmissionSlots
	t.Cleanup(func() { mistTriggerProcessor = previous; publisherAdmissionSlots = previousSlots })
	stream := &captureStream{}
	t.Cleanup(SetupTestRegistry("node", stream))
	c := registry.conns["node"]
	c.canonicalID, c.clusterID, c.fence = "node", "cluster", 1
	c.admissionToken = bytes.Repeat([]byte{2}, 32)
	req := &ipcpb.AdmitPublisherRequest{NodeId: "node", SessionToken: c.admissionToken, Trigger: &ipcpb.MistTrigger{TriggerType: "PUSH_REWRITE", TriggerPayload: &ipcpb.MistTrigger_PushRewrite{PushRewrite: &ipcpb.PushRewriteTrigger{Pid: 1, TriggerUuid: "exec"}}}}
	mistTriggerProcessor = publisherProcessorFunc(func(context.Context, *ipcpb.MistTrigger) (string, bool, error) {
		t.Fatal("cancelled queue entered coordinator")
		return "", true, nil
	})
	synctest.Test(t, func(t *testing.T) {
		publisherAdmissionSlots = make(chan struct{})
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
		defer cancel()
		if _, err := (&Server{}).AdmitPublisher(ctx, req); status.Code(err) != codes.DeadlineExceeded {
			t.Fatalf("queue ignored cancellation: %v", err)
		}
	})
	publisherAdmissionSlots = make(chan struct{}, 1)
	mistTriggerProcessor = publisherProcessorFunc(func(context.Context, *ipcpb.MistTrigger) (string, bool, error) {
		registry.mu.Lock()
		delete(registry.conns, "node")
		registry.mu.Unlock()
		return "live+stream", false, nil
	})
	ctx2, cancel2 := context.WithTimeout(t.Context(), time.Second)
	defer cancel2()
	if _, err := (&Server{}).AdmitPublisher(ctx2, req); status.Code(err) != codes.Unavailable {
		t.Fatalf("replaced connection received accept: %v", err)
	}
}

func TestPublisherRPCUsesSessionCapabilityThroughProductionInterceptors(t *testing.T) {
	previous := mistTriggerProcessor
	t.Cleanup(func() { mistTriggerProcessor = previous })
	t.Cleanup(SetupTestRegistry("edge-unary", &captureStream{}))
	connection := registry.conns["edge-unary"]
	connection.canonicalID, connection.clusterID, connection.fence = "edge-unary", "media", 1
	connection.admissionToken = bytes.Repeat([]byte{3}, 32)
	mistTriggerProcessor = publisherProcessorFunc(func(_ context.Context, trigger *ipcpb.MistTrigger) (string, bool, error) {
		trigger.EventId = "generation"
		return "live+stream", false, nil
	})
	listener := bufconn.Listen(1 << 20)
	t.Cleanup(func() { _ = listener.Close() })
	server := grpc.NewServer(appendCommonInterceptors(nil, GRPCServerConfig{ServiceToken: "internal-service-token", Logger: logging.NewLogger()})...)
	ipcpb.RegisterHelmsmanControlServer(server, &Server{})
	t.Cleanup(server.Stop)
	go func() { _ = server.Serve(listener) }()
	clientConnection, err := grpc.NewClient("passthrough:///publisher", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientConnection.Close() })
	client := ipcpb.NewHelmsmanControlClient(clientConnection)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	request := &ipcpb.AdmitPublisherRequest{NodeId: "edge-unary", SessionToken: bytes.Clone(connection.admissionToken), Trigger: &ipcpb.MistTrigger{TriggerType: "PUSH_REWRITE", TriggerPayload: &ipcpb.MistTrigger_PushRewrite{PushRewrite: &ipcpb.PushRewriteTrigger{Pid: 4, TriggerUuid: "execution"}}}}
	response, err := client.AdmitPublisher(ctx, request)
	if err != nil || response.GetIngestGeneration() != "generation" {
		t.Fatalf("valid node capability rejected by production interceptor chain: %v %v", response, err)
	}
	request.SessionToken[0] ^= 1
	if _, err := client.AdmitPublisher(ctx, request); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("forged capability allowed: %v", err)
	}
}

func TestLegacyPublisherContextBindsSettlementCapabilityAndLifetime(t *testing.T) {
	for _, test := range []struct {
		name     string
		deadline *timestamppb.Timestamp
		legacy   bool
	}{
		{"old edge", nil, true},
		{"invalid deadline", &timestamppb.Timestamp{Seconds: 253402300800}, true},
		{"capable edge", timestamppb.New(time.Now().Add(time.Second)), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			parent, cancelParent := context.WithTimeout(t.Context(), 500*time.Millisecond)
			defer cancelParent()
			ctx, cancel := LegacyPublisherContext(parent, &ipcpb.MistTrigger{PublisherDeadline: test.deadline})
			defer cancel()
			if got := PublisherSettlementRequiresCompatibility(ctx); got != test.legacy {
				t.Fatalf("legacy settlement=%v want %v", got, test.legacy)
			}
			got, _ := ctx.Deadline()
			want, _ := parent.Deadline()
			if !got.Equal(want) {
				t.Fatalf("legacy adapter extended caller lifetime: got %v want %v", got, want)
			}
		})
	}
}
