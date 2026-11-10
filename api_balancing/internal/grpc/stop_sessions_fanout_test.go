package grpc

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

type concurrentStopStream struct {
	ipcpb.HelmsmanControl_ConnectServer
	entered chan<- struct{}
	release <-chan struct{}
}

func (s *concurrentStopStream) Send(message *ipcpb.ControlMessage) error {
	if request := message.GetStopSessionsRequest(); request != nil {
		s.entered <- struct{}{}
		<-s.release
		control.CompleteTestStopSessions(request.GetNodeId(), request, "")
	}
	return nil
}

func TestTerminateTenantStreamsDispatchesNodesConcurrently(t *testing.T) {
	manager := state.ResetDefaultManagerForTests()
	t.Cleanup(manager.Shutdown)
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}, 2), make(chan struct{})
		streams := map[string]ipcpb.HelmsmanControl_ConnectServer{
			"node-a": &concurrentStopStream{entered: entered, release: release},
			"node-b": &concurrentStopStream{entered: entered, release: release},
		}
		t.Cleanup(control.SetupTestRegistryNodes(streams))
		for node := range streams {
			seedLiveStreamRpcMore(t, manager, "stream-"+node, node, "tenant")
		}
		go func() {
			<-entered
			<-entered
			close(release)
		}()
		server := NewFoghornGRPCServer(nil, logging.NewLogger(), balancer.NewLoadBalancer(logging.NewLogger()), nil, nil, nil, nil, nil)
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		response, err := server.TerminateTenantStreams(ctx, &foghorncontrolpb.TerminateTenantStreamsRequest{TenantId: "tenant", Reason: "account_inactive"})
		synctest.Wait()
		if err != nil || response.GetSessionsTerminated() != 2 {
			t.Fatalf("one node consumed another node's stop budget: response=%v err=%v", response, err)
		}
	})
}
