package grpc

import (
	"testing"

	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"google.golang.org/protobuf/proto"
)

func TestChapterViewerRequestPreservesAdmissionContext(t *testing.T) {
	original := &sharedpb.ViewerEndpointRequest{
		ContentId: "recording", Protocol: "dash",
		ViewerIp: proto.String("203.0.113.1"), ViewerToken: proto.String("viewer-token"),
	}
	wantOriginal := proto.Clone(original)
	chapter := chapterViewerRequest(original, "chapter")
	wantChapter := proto.CloneOf(original)
	wantChapter.ContentId = "chapter"
	if !proto.Equal(chapter, wantChapter) {
		t.Fatalf("chapter resolution changed viewer requirements: %v", chapter)
	}
	*chapter.ViewerToken = "changed"
	if !proto.Equal(original, wantOriginal) {
		t.Fatal("chapter request aliases the caller's request")
	}
}
