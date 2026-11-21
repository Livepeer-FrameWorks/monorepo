package resolvers

import (
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"testing"
)

func TestProcessingWaitReasonSurvivesGraphQLMappings(t *testing.T) {
	reason := "memory"
	catalog := &commodorepb.StorageArtifactInfo{Kind: "vod", Id: "asset", ArtifactHash: "asset", Status: "queued", ProcessingWaitReason: &reason}
	r := &Resolver{}
	artifact, err := r.storageArtifactFromProto(t.Context(), catalog)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.ProcessingWaitReason == nil || *artifact.ProcessingWaitReason != reason {
		t.Fatal("storage artifact lost the queued reason")
	}
	asset := storageArtifactToVodAsset(catalog)
	if asset.ProcessingWaitReason == nil || *asset.ProcessingWaitReason != reason {
		t.Fatal("catalog VOD lost the queued reason")
	}
	legacy := protoToVodAsset(&sharedpb.VodAssetInfo{Id: "asset", ArtifactHash: "asset", ProcessingWaitReason: &reason})
	if legacy.ProcessingWaitReason == nil || *legacy.ProcessingWaitReason != reason {
		t.Fatal("upload response lost the queued reason")
	}
}
