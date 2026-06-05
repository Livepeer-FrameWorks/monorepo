package grpc

import (
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	signalmanpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/signalman"
)

func newTestClient(buf int, channels ...signalmanpb.Channel) *Client {
	return &Client{
		channels: channels,
		send:     make(chan *signalmanpb.ServerMessage, buf),
		logger:   logging.NewLogger(),
	}
}

// TestHandleUnsubscribe_RemovesChannelAndConfirms pins that unsubscribing one
// channel leaves the others intact and sends a confirmation listing exactly the
// remaining subscriptions.
func TestHandleUnsubscribe_RemovesChannelAndConfirms(t *testing.T) {
	c := newTestClient(4, signalmanpb.Channel_CHANNEL_STREAMS, signalmanpb.Channel_CHANNEL_SYSTEM)

	c.handleUnsubscribe(&signalmanpb.UnsubscribeRequest{
		Channels: []signalmanpb.Channel{signalmanpb.Channel_CHANNEL_SYSTEM},
	})

	if len(c.channels) != 1 || c.channels[0] != signalmanpb.Channel_CHANNEL_STREAMS {
		t.Fatalf("remaining channels = %v, want [CHANNEL_STREAMS]", c.channels)
	}

	select {
	case msg := <-c.send:
		conf := msg.GetSubscriptionConfirmed()
		if conf == nil {
			t.Fatalf("expected SubscriptionConfirmed, got %T", msg.GetMessage())
		}
		got := conf.GetSubscribedChannels()
		if len(got) != 1 || got[0] != signalmanpb.Channel_CHANNEL_STREAMS {
			t.Errorf("confirmation channels = %v, want [CHANNEL_STREAMS]", got)
		}
	default:
		t.Fatal("expected an unsubscribe confirmation on the send channel")
	}
}

// TestHandleUnsubscribe_UnknownChannelIsNoop pins that unsubscribing a channel
// the client never had leaves the subscription set unchanged.
func TestHandleUnsubscribe_UnknownChannelIsNoop(t *testing.T) {
	c := newTestClient(4, signalmanpb.Channel_CHANNEL_STREAMS)

	c.handleUnsubscribe(&signalmanpb.UnsubscribeRequest{
		Channels: []signalmanpb.Channel{signalmanpb.Channel_CHANNEL_SYSTEM},
	})

	if len(c.channels) != 1 || c.channels[0] != signalmanpb.Channel_CHANNEL_STREAMS {
		t.Fatalf("channels = %v, want unchanged [CHANNEL_STREAMS]", c.channels)
	}
	// Still confirms (with the unchanged set).
	select {
	case msg := <-c.send:
		if got := msg.GetSubscriptionConfirmed().GetSubscribedChannels(); len(got) != 1 {
			t.Errorf("confirmation channels = %v, want 1", got)
		}
	default:
		t.Fatal("expected a confirmation even for a no-op unsubscribe")
	}
}

// TestHandlePing_EchoesTimestamp pins the pong round-trip: the client's ping
// timestamp is echoed back so the dashboard can measure RTT.
func TestHandlePing_EchoesTimestamp(t *testing.T) {
	c := newTestClient(1)

	c.handlePing(&signalmanpb.Ping{TimestampMs: 987654})

	select {
	case msg := <-c.send:
		pong := msg.GetPong()
		if pong == nil {
			t.Fatalf("expected Pong, got %T", msg.GetMessage())
		}
		if pong.GetTimestampMs() != 987654 {
			t.Errorf("pong timestamp = %d, want 987654 (echoed)", pong.GetTimestampMs())
		}
	default:
		t.Fatal("expected a pong on the send channel")
	}
}

// TestHandlePing_FullBufferDoesNotBlock pins the non-blocking send: a full
// buffer drops the pong rather than stalling the client goroutine.
func TestHandlePing_FullBufferDoesNotBlock(t *testing.T) {
	c := newTestClient(0) // unbuffered + no reader → select default fires
	done := make(chan struct{})
	go func() {
		c.handlePing(&signalmanpb.Ping{TimestampMs: 1})
		close(done)
	}()
	select {
	case <-done:
		// returned without blocking
	case <-time.After(time.Second):
		t.Fatal("handlePing blocked on a full send buffer")
	}
}
