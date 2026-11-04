package control

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/grpc"
)

type publisherUnaryClient struct {
	ipcpb.HelmsmanControlClient
	call func(context.Context, *ipcpb.AdmitPublisherRequest) (*ipcpb.MistTriggerResponse, error)
}

func (c publisherUnaryClient) AdmitPublisher(ctx context.Context, req *ipcpb.AdmitPublisherRequest, _ ...grpc.CallOption) (*ipcpb.MistTriggerResponse, error) {
	return c.call(ctx, req)
}

func TestPublisherUnaryCarriesCallerLifetimeAndDoesNotReplayAmbiguousResult(t *testing.T) {
	resetControlState(t)
	stream := &fakeControlStream{sendCh: make(chan *ipcpb.ControlMessage, 4)}
	connection := storeConn(stream, "edge-1")
	token := make([]byte, 32)
	token[0] = 1
	connection.publisher.session.Store(&ipcpb.PublisherAdmissionSession{NodeId: "edge-1", Token: token})
	ctx, cancel := context.WithTimeout(t.Context(), 80*time.Millisecond)
	defer cancel()
	expected, _ := ctx.Deadline()
	calls := 0
	connection.publisher.client = publisherUnaryClient{call: func(callCtx context.Context, req *ipcpb.AdmitPublisherRequest) (*ipcpb.MistTriggerResponse, error) {
		calls++
		deadline, ok := callCtx.Deadline()
		if !ok || !deadline.Equal(expected) {
			t.Fatalf("caller deadline changed: %v", deadline)
		}
		if req.GetNodeId() != "edge-1" || len(req.GetSessionToken()) != 32 || req.GetSessionToken()[0] != 1 {
			t.Fatal("session capability missing")
		}
		<-callCtx.Done()
		return nil, callCtx.Err()
	}}
	trigger := pushRewriteForSettle("unary-execution", 19, time.Now())
	result, err := SendMistTriggerContext(ctx, trigger, logging.NewLogger())
	if !errors.Is(err, context.DeadlineExceeded) || result == nil || !result.Abort || !result.Forwarded {
		t.Fatalf("unknown result lost its settlement identity: %v %v", result, err)
	}
	if calls != 1 || len(stream.sendCh) != 0 {
		t.Fatalf("ambiguous unary was replayed: calls=%d stream=%d", calls, len(stream.sendCh))
	}
}
