package livepeer

import (
	"context"
	"encoding/hex"
	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
	lp "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/livepeer_observer"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"net"
	"strconv"
	"testing"
	"time"
)

type orchTest interface{}

func TestProbePinsIPAndPreservesAuthority(t *testing.T) {
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	authority := "unresolvable.invalid:" + strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	_ = authority
	server := grpc.NewServer()
	address := make([]byte, 20)
	server.RegisterService(&grpc.ServiceDesc{ServiceName: "net.Orchestrator", HandlerType: (*orchTest)(nil), Methods: []grpc.MethodDesc{{MethodName: "GetOrchestrator", Handler: func(_ any, ctx context.Context, decode func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
		var req lp.DiscoveryRequest
		if decodeErr := decode(&req); decodeErr != nil {
			return nil, decodeErr
		}
		md, _ := metadata.FromIncomingContext(ctx)
		if md.Get(":authority")[0] != authority {
			t.Errorf("authority changed: %v", md)
		}
		if !req.IgnoreCapacityCheck {
			t.Error("discovery must ignore transient job capacity")
		}
		return &lp.DiscoveryResponse{Transcoder: "http://" + authority, Address: address, PriceInfo: &lp.DiscoveryPrice{PricePerUnit: 4, PixelsPerUnit: 10}}, nil
	}}}}, struct{}{})
	go server.Serve(listener)
	defer server.Stop()
	resource := obs.Resource{ID: "0x" + hex.EncodeToString(address), URI: "http://" + authority}
	endpoint, err := obs.NewEndpoint(resource, "127.0.0.1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	p := Probe{Address: address, Signature: make([]byte, 65), Timeout: 2 * time.Second}
	result := p.Check(context.Background(), obs.Assignment{Endpoint: endpoint, ObserverID: "test", Deadline: time.Now().Add(time.Minute)})
	if !result.Check.Success || result.Check.ActualIP != "127.0.0.1" || result.Quote == nil {
		t.Fatalf("pinned discovery failed: %+v", result)
	}
}
