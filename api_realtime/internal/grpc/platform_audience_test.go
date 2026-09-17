package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	signalmanpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/signalman"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/tenants"

	"google.golang.org/grpc/metadata"
)

func platformIncidentData(tenantID string) *signalmanpb.EventData {
	return &signalmanpb.EventData{Payload: &signalmanpb.EventData_IncidentUpdated{IncidentUpdated: &ipcpb.IncidentEvent{
		IncidentId: "incident-1", TenantId: tenantID, Status: "firing", Change: "opened",
	}}}
}

// openBufConnStream subscribes over the real auth interceptor with the given
// metadata and returns the stream after its subscription confirmation, plus
// any error message sent before it.
func openBufConnStream(t *testing.T, client signalmanpb.SignalmanServiceClient, md metadata.MD, channels ...signalmanpb.Channel) (signalmanpb.SignalmanService_SubscribeClient, *signalmanpb.SignalmanError, []signalmanpb.Channel) {
	t.Helper()
	ctx, cancel := context.WithCancel(metadata.NewOutgoingContext(context.Background(), md))
	t.Cleanup(cancel)
	stream, err := client.Subscribe(ctx)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := stream.Send(&signalmanpb.ClientMessage{Message: &signalmanpb.ClientMessage_Subscribe{
		Subscribe: &signalmanpb.SubscribeRequest{Channels: channels},
	}}); err != nil {
		t.Fatalf("send subscribe: %v", err)
	}
	var refusal *signalmanpb.SignalmanError
	for {
		msg, err := stream.Recv()
		if err != nil {
			t.Fatalf("receive confirmation: %v", err)
		}
		if msg.GetError() != nil {
			refusal = msg.GetError()
			continue
		}
		if conf := msg.GetSubscriptionConfirmed(); conf != nil {
			return stream, refusal, conf.GetSubscribedChannels()
		}
	}
}

func receiveEvent(t *testing.T, stream signalmanpb.SignalmanService_SubscribeClient, timeout time.Duration) *signalmanpb.SignalmanEvent {
	t.Helper()
	events := make(chan *signalmanpb.SignalmanEvent, 1)
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				close(events)
				return
			}
			if event := msg.GetEvent(); event != nil {
				events <- event
				return
			}
		}
	}()
	select {
	case event := <-events:
		return event
	case <-time.After(timeout):
		return nil
	}
}

// Only a tenantless service-token stream receives platform events. A service
// stream carrying a tenant, including the system tenant, is refused the
// channel and never receives platform events, not even through CHANNEL_ALL or
// the system channel.
func TestPlatformChannelReachesOnlyTenantlessServiceStreams(t *testing.T) {
	server := NewSignalmanServer(logging.NewLogger(), nil)
	client, cleanup := newBufConnClient(t, server, "service-token")
	defer cleanup()

	operator, refusal, channels := openBufConnStream(t, client,
		metadata.Pairs("authorization", "Bearer service-token"),
		signalmanpb.Channel_CHANNEL_PLATFORM)
	if refusal != nil || len(channels) != 1 || channels[0] != signalmanpb.Channel_CHANNEL_PLATFORM {
		t.Fatalf("tenantless service stream: refusal=%v channels=%v, want platform confirmed", refusal, channels)
	}

	tenantStreams := map[string]signalmanpb.SignalmanService_SubscribeClient{}
	for name, tenantID := range map[string]string{
		"system tenant": tenants.SystemTenantID.String(),
		"tenant":        "7a1d0000-0000-4000-8000-000000000001",
	} {
		stream, refusal, channels := openBufConnStream(t, client,
			metadata.Pairs("authorization", "Bearer service-token", "x-tenant-id", tenantID),
			signalmanpb.Channel_CHANNEL_PLATFORM, signalmanpb.Channel_CHANNEL_ALL, signalmanpb.Channel_CHANNEL_SYSTEM)
		if refusal == nil || refusal.GetCode() != "permission_denied" {
			t.Fatalf("%s stream platform subscription refusal = %v, want permission_denied", name, refusal)
		}
		for _, channel := range channels {
			if channel == signalmanpb.Channel_CHANNEL_PLATFORM {
				t.Fatalf("%s stream confirmed channels %v include platform", name, channels)
			}
		}
		tenantStreams[name] = stream
	}
	waitForClients(t, server.hub, 3)

	server.hub.BroadcastPlatform(signalmanpb.EventType_EVENT_TYPE_INCIDENT_UPDATED, platformIncidentData(tenants.SystemTenantID.String()))
	event := receiveEvent(t, operator, 2*time.Second)
	if event == nil || event.GetChannel() != signalmanpb.Channel_CHANNEL_PLATFORM || event.TenantId != nil ||
		event.GetData().GetIncidentUpdated().GetIncidentId() != "incident-1" {
		t.Fatalf("platform audience event = %v, want the platform incident", event)
	}
	for name, stream := range tenantStreams {
		if leaked := receiveEvent(t, stream, 150*time.Millisecond); leaked != nil {
			t.Fatalf("%s stream received platform event %v", name, leaked)
		}
	}
}

// A platform operator's own JWT does not make a stream part of the platform
// audience: operators reach it only through the Gateway's service stream.
func TestPlatformChannelRefusesUserStreams(t *testing.T) {
	server := NewSignalmanServer(logging.NewLogger(), nil)
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "jwt")
	ctx = context.WithValue(ctx, ctxkeys.KeyPlatformOperator, true)
	stream := newFakeSignalmanStream(ctx)
	errCh := make(chan error, 1)
	go func() { errCh <- server.Subscribe(stream) }()
	stream.recvCh <- &signalmanpb.ClientMessage{Message: &signalmanpb.ClientMessage_Subscribe{
		Subscribe: &signalmanpb.SubscribeRequest{Channels: []signalmanpb.Channel{signalmanpb.Channel_CHANNEL_PLATFORM, signalmanpb.Channel_CHANNEL_ALL}},
	}}
	if refusal := receiveMessage(t, stream, 2*time.Second).GetError(); refusal.GetCode() != "permission_denied" {
		t.Fatalf("user stream platform refusal = %v, want permission_denied", refusal)
	}
	if channels := receiveMessage(t, stream, 2*time.Second).GetSubscriptionConfirmed().GetSubscribedChannels(); len(channels) != 1 || channels[0] != signalmanpb.Channel_CHANNEL_ALL {
		t.Fatalf("user stream confirmed channels = %v, want only CHANNEL_ALL", channels)
	}

	server.hub.BroadcastPlatform(signalmanpb.EventType_EVENT_TYPE_INCIDENT_UPDATED, platformIncidentData(""))
	select {
	case msg := <-stream.sendCh:
		t.Fatalf("user stream received %v after a platform broadcast", msg.GetMessage())
	case <-time.After(150 * time.Millisecond):
	}
	close(stream.recvCh)
	<-errCh
}
