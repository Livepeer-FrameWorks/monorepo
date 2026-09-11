package commodore

import (
	"context"
	"testing"

	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"google.golang.org/grpc"
)

type ingestProtocolViewer struct {
	commodorepb.ViewerServiceClient
	request       *sharedpb.IngestEndpointRequest
	viewerRequest *sharedpb.ViewerEndpointRequest
}

func (viewer *ingestProtocolViewer) ResolveViewerEndpoint(_ context.Context, req *sharedpb.ViewerEndpointRequest, _ ...grpc.CallOption) (*sharedpb.ViewerEndpointResponse, error) {
	viewer.viewerRequest = req
	return &sharedpb.ViewerEndpointResponse{}, nil
}

func TestResolveViewerEndpointForwardsProtocol(t *testing.T) {
	viewer := &ingestProtocolViewer{}
	client := &GRPCClient{viewer: viewer}
	_, err := client.ResolveViewerEndpointWithProtocol(t.Context(), "public", "192.0.2.1", "viewer-token", "whep")
	if err != nil || viewer.viewerRequest.GetProtocol() != "whep" || viewer.viewerRequest.GetContentId() != "public" || viewer.viewerRequest.GetViewerIp() != "192.0.2.1" || viewer.viewerRequest.GetViewerToken() != "viewer-token" {
		t.Fatalf("forwarded request=%+v err=%v", viewer.viewerRequest, err)
	}
	_, err = client.ResolveViewerEndpoint(t.Context(), "public", "192.0.2.1", "viewer-token")
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
	client := &GRPCClient{viewer: viewer}
	_, err := client.ResolveIngestEndpoint(t.Context(), "key", "192.0.2.1", sharedpb.IngestProtocol_INGEST_PROTOCOL_SRT)
	if err != nil || viewer.request.GetProtocol() != sharedpb.IngestProtocol_INGEST_PROTOCOL_SRT || viewer.request.GetStreamKey() != "key" || viewer.request.GetViewerIp() != "192.0.2.1" {
		t.Fatalf("forwarded request=%+v err=%v", viewer.request, err)
	}
}
