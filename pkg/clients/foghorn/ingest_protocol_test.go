package foghorn

import (
	"context"
	"testing"
	"time"

	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"google.golang.org/grpc"
)

type ingestProtocolViewer struct {
	foghornpb.ViewerControlServiceClient
	request       *sharedpb.IngestEndpointRequest
	viewerRequest *sharedpb.ViewerEndpointRequest
}

func (viewer *ingestProtocolViewer) ResolveViewerEndpoint(_ context.Context, req *sharedpb.ViewerEndpointRequest, _ ...grpc.CallOption) (*sharedpb.ViewerEndpointResponse, error) {
	viewer.viewerRequest = req
	return &sharedpb.ViewerEndpointResponse{}, nil
}

func TestResolveViewerEndpointForwardsProtocol(t *testing.T) {
	viewer := &ingestProtocolViewer{}
	client := &GRPCClient{viewer: viewer, timeout: time.Second}
	ip, token := "192.0.2.1", "viewer-token"
	_, _, err := client.ResolveViewerEndpointWithProtocol(t.Context(), "public", &ip, &token, "whep")
	if err != nil || viewer.viewerRequest.GetProtocol() != "whep" || viewer.viewerRequest.GetContentId() != "public" || viewer.viewerRequest.GetViewerIp() != ip || viewer.viewerRequest.GetViewerToken() != token {
		t.Fatalf("forwarded request=%+v err=%v", viewer.viewerRequest, err)
	}
	_, _, err = client.ResolveViewerEndpoint(t.Context(), "public", &ip, &token)
	if err != nil || viewer.viewerRequest.GetProtocol() != "" {
		t.Fatal("unspecified protocol changed during forwarding")
	}
}

func (viewer *ingestProtocolViewer) ResolveIngestEndpoint(_ context.Context, req *sharedpb.IngestEndpointRequest, _ ...grpc.CallOption) (*sharedpb.IngestEndpointResponse, error) {
	viewer.request = req
	return &sharedpb.IngestEndpointResponse{}, nil
}

func TestResolveIngestEndpointForwardsProtocol(t *testing.T) {
	viewer := &ingestProtocolViewer{}
	client := &GRPCClient{viewer: viewer, timeout: time.Second}
	ip := "192.0.2.1"
	_, _, err := client.ResolveIngestEndpoint(t.Context(), "key", &ip, sharedpb.IngestProtocol_INGEST_PROTOCOL_WHIP)
	if err != nil || viewer.request.GetProtocol() != sharedpb.IngestProtocol_INGEST_PROTOCOL_WHIP || viewer.request.GetStreamKey() != "key" || viewer.request.GetViewerIp() != ip {
		t.Fatalf("forwarded request=%+v err=%v", viewer.request, err)
	}
}
