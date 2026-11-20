package resolvers

import (
	"context"
	"testing"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients/clientstest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
)

func TestStreamMediaReadsRequireStreamsScope(t *testing.T) {
	for _, scope := range []string{"billing:read", "analytics:read"} {
		t.Run(scope, func(t *testing.T) {
			resolver := commoB3(&clientstest.FakeCommodore{})
			ctx := context.WithValue(clientstest.AuthedCtx("tenant"), ctxkeys.KeyAuthType, "api_token")
			ctx = context.WithValue(ctx, ctxkeys.KeyPermissions, []string{scope})
			if _, err := resolver.DoStreamArtifacts(ctx, &pb.Stream{StreamId: "stream"}, "dvr", nil); err == nil {
				t.Fatal("recordings bypassed streams:read")
			}
			if _, err := resolver.DoStreamArtifacts(ctx, &pb.Stream{StreamId: "stream"}, "clip", nil); err == nil {
				t.Fatal("clips bypassed streams:read")
			}
			if _, err := resolver.DoGetDVRRecording(ctx, "recording"); err == nil {
				t.Fatal("recording bypassed streams:read")
			}
			if _, err := resolver.DoSavedDVRChapters(ctx, &sharedpb.DVRInfo{DvrHash: "recording"}, nil, nil, nil); err == nil {
				t.Fatal("chapters bypassed streams:read")
			}
		})
	}
}

func TestMediaPaginationRejectsAmbiguousAndInvalidPages(t *testing.T) {
	two := 2
	zero := 0
	cursor := "malformed"
	for _, page := range []*model.ConnectionInput{{First: &two, Last: &two}, {Before: &cursor}, {First: &zero}, {After: &cursor}} {
		if _, err := mediaPagination(page); err == nil {
			t.Fatalf("accepted invalid media page: %v", page)
		}
	}
}
