package resolvers

import (
	"context"
	"testing"

	"frameworks/pkg/ctxkeys"
	pb "frameworks/pkg/proto"
)

func TestDemoVodUploadStatusDoesNotRequireTenantContext(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyDemoMode, true)
	ctx = context.WithValue(ctx, ctxkeys.KeyAuthType, "jwt")

	resp, err := (&Resolver{}).DoGetVodUploadStatusProto(ctx, "demo_upload_active")
	if err != nil {
		t.Fatalf("DoGetVodUploadStatusProto: %v", err)
	}
	if resp.State != pb.VodStatus_VOD_STATUS_UPLOADING {
		t.Fatalf("State = %v, want uploading", resp.State)
	}
	if len(resp.UploadedParts) == 0 || len(resp.MissingParts) == 0 {
		t.Fatalf("expected uploaded and missing multipart state, got uploaded=%d missing=%d", len(resp.UploadedParts), len(resp.MissingParts))
	}
	if resp.ArtifactHash == "" || resp.PlaybackId == "" {
		t.Fatal("expected artifact hash and playback id")
	}
}
