package resolvers

import (
	"context"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
)

func TestDemoVodUploadStatusDoesNotRequireTenantContext(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyDemoMode, true)
	ctx = context.WithValue(ctx, ctxkeys.KeyAuthType, "jwt")

	resp, err := (&Resolver{}).DoGetVodUploadStatusProto(ctx, "demo_upload_active")
	if err != nil {
		t.Fatalf("DoGetVodUploadStatusProto: %v", err)
	}
	if resp.State != sharedpb.VodStatus_VOD_STATUS_UPLOADING {
		t.Fatalf("State = %v, want uploading", resp.State)
	}
	if len(resp.UploadedParts) == 0 || len(resp.MissingParts) == 0 {
		t.Fatalf("expected uploaded and missing multipart state, got uploaded=%d missing=%d", len(resp.UploadedParts), len(resp.MissingParts))
	}
	if resp.ArtifactHash == "" || resp.PlaybackId == "" {
		t.Fatal("expected artifact hash and playback id")
	}
}
