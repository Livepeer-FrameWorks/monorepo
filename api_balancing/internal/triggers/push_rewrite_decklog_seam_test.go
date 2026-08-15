package triggers

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/decklog"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

// decklogCapture is a real Decklog gRPC receiver that keeps every trigger it is
// sent. Asserting on the wire payload — rather than on the redaction helper —
// is what makes this a test of the security boundary: it fails if the
// sanitization is removed, reordered after the send, or bypassed.
type decklogCapture struct {
	ipcpb.UnimplementedDecklogServiceServer

	mu       sync.Mutex
	triggers []*ipcpb.MistTrigger
}

func (d *decklogCapture) SendEvent(_ context.Context, trigger *ipcpb.MistTrigger) (*emptypb.Empty, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.triggers = append(d.triggers, proto.Clone(trigger).(*ipcpb.MistTrigger))
	return &emptypb.Empty{}, nil
}

func (d *decklogCapture) received() []*ipcpb.MistTrigger {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]*ipcpb.MistTrigger, len(d.triggers))
	copy(out, d.triggers)
	return out
}

// startDecklogCapture serves the capture on a localhost listener and returns a
// real batched client pointed at it.
func startDecklogCapture(t *testing.T) (*decklogCapture, *decklog.BatchedClient) {
	t.Helper()

	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	capture := &decklogCapture{}
	srv := grpc.NewServer()
	ipcpb.RegisterDecklogServiceServer(srv, capture)
	go func() { _ = srv.Serve(lis) }()

	client, err := decklog.NewBatchedClient(decklog.BatchedClientConfig{
		Target:        lis.Addr().String(),
		AllowInsecure: true,
		Source:        "foghorn-test",
		Timeout:       5 * time.Second,
	}, logging.NewLogger())
	if err != nil {
		srv.Stop()
		_ = lis.Close()
		t.Fatalf("decklog client: %v", err)
	}

	t.Cleanup(func() {
		srv.Stop()
		_ = lis.Close()
	})
	return capture, client
}

// The trigger Foghorn puts on the wire is persisted by Periscope into
// ClickHouse, so no field of it may carry the publishing credential.
//
// This drives the real handlePushRewrite and inspects what Decklog actually
// received, including the serialized bytes: the payload is stored whole, so a
// key hiding in an unchecked field would still land in event_data. Because the
// assertion is on the received message rather than on the redaction helper, it
// fails if the sanitization is removed, or moved to after the send.
func TestPushRewriteTriggerCarriesNoCredentialOnTheWire(t *testing.T) {
	decklogBytes := installIngestSessionMintMockCaptureDecklog(t)
	const (
		rawKey       = "sk_live_secret_key_value"
		internalName = "resolved-internal-name"
	)

	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	sm.SetNodeInfo("edge-node-1", "http://edge.example/view", true, nil, nil, "", "", nil)
	// PUSH_REWRITE claims placement under the publishing node's virtual media
	// cluster, so the node has to be attributable to one.
	sm.SetNodeConnectionInfo(context.Background(), "edge-node-1", "edge-node-1:18090", "", "demo-media", nil)
	prevRegistry := control.StreamRegistryInstance
	control.SetStreamRegistry(control.NewStreamRegistry(nil, "cluster-local", time.Minute))
	t.Cleanup(func() { control.SetStreamRegistry(prevRegistry) })

	commodoreClient, cleanup := setupCommodoreClient(t, &commodorepb.ValidateStreamKeyResponse{
		Valid:        true,
		UserId:       "user-1",
		TenantId:     "tenant-1",
		InternalName: internalName,
		StreamId:     "stream-1",
		BillingModel: "postpaid",
	}, nil)
	t.Cleanup(cleanup)

	capture, client := startDecklogCapture(t)

	p := newTestProcessor(t)
	p.commodoreClient = commodoreClient
	p.decklogClient = client
	p.clusterID = "cluster-local"

	trigger := &ipcpb.MistTrigger{
		NodeId: "edge-node-1",
		TriggerPayload: &ipcpb.MistTrigger_PushRewrite{
			PushRewrite: &ipcpb.PushRewriteTrigger{Pid: 4242, TriggerUuid: "test-trigger-uuid", TriggerUnixMillis: 1,
				// What Mist reports: the publishing credential as the name.
				StreamName: rawKey,
				PushUrl:    "rtmp://edge-ingest.example.com:1935/live/" + rawKey,
				Hostname:   "203.0.113.9",
			},
		},
	}

	streamName, blocking, err := p.handlePushRewrite(trigger)
	if err != nil {
		t.Fatalf("handlePushRewrite: %v", err)
	}
	if blocking {
		t.Fatal("expected the push to be admitted")
	}
	if streamName != "live+"+internalName {
		t.Fatalf("rewrite response = %q, want live+%s", streamName, internalName)
	}

	// The ingest event is a leg of the durable admission obligation, not an inline send: drive the
	// worker leg with the exact persisted bytes, then assert on what Decklog actually receives.
	if len(capture.received()) != 0 {
		t.Fatal("the trigger goroutine must not send the ingest event inline (obligation-owned)")
	}
	persisted := decklogBytes.bytes()
	if len(persisted) == 0 {
		t.Fatal("the confirmation did not persist the Decklog leg")
	}
	if _, applyErr := p.ApplyAdmissionEffect(context.Background(), control.AdmissionEffect{
		TenantID:       "tenant-1",
		InternalName:   internalName,
		NodeID:         "edge-node-1",
		DecklogTrigger: persisted,
	}); applyErr != nil {
		t.Fatalf("ApplyAdmissionEffect (Decklog leg): %v", applyErr)
	}
	var received []*ipcpb.MistTrigger
	for range 50 {
		if received = capture.received(); len(received) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(received) == 0 {
		t.Fatal("decklog received no trigger")
	}
	if got := received[0].GetEventId(); got != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("event_id = %q, want the deterministic generation", got)
	}

	got := received[0].GetPushRewrite()
	if got.GetStreamName() != "live+"+internalName {
		t.Errorf("stream_name on the wire = %q, want the resolved name", got.GetStreamName())
	}
	if strings.Contains(got.GetPushUrl(), rawKey) {
		t.Errorf("push_url still carries the key: %q", got.GetPushUrl())
	}
	if !strings.Contains(got.GetPushUrl(), "edge-ingest.example.com") {
		t.Errorf("push_url lost its host, analytics needs it: %q", got.GetPushUrl())
	}

	// Periscope stores the whole payload, so check the bytes rather than a
	// field list that could go stale as the message grows.
	wire, err := proto.Marshal(received[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(wire), rawKey) {
		t.Fatal("serialized trigger contains the raw stream key")
	}
}
