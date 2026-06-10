package resolvers

import (
	"context"
	"errors"
	"testing"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients/clientstest"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// DoCreateVodUpload stamps tenant from context onto the outbound request and maps
// the presigned-upload response onto the VodUploadSession model.
func TestDoCreateVodUpload(t *testing.T) {
	var gotReq *sharedpb.CreateVodUploadRequest
	commo := &clientstest.FakeCommodore{
		CreateVodUploadFn: func(_ context.Context, req *sharedpb.CreateVodUploadRequest) (*sharedpb.CreateVodUploadResponse, error) {
			gotReq = req
			return &sharedpb.CreateVodUploadResponse{
				UploadId:     "up1",
				ArtifactId:   "art1",
				ArtifactHash: "hash1",
				PlaybackId:   "pl1",
				PartSize:     5242880,
				ExpiresAt:    timestamppb.Now(),
			}, nil
		},
	}
	r := commoR(commo)
	ct := "video/mp4"
	res, err := r.DoCreateVodUpload(clientstest.AuthedCtx("t1"), model.CreateVodUploadInput{
		Filename:    "movie.mp4",
		SizeBytes:   1000,
		ContentType: &ct,
	})
	if err != nil {
		t.Fatalf("DoCreateVodUpload err: %v", err)
	}
	// Tenant is server-derived; filename + size ride the request from input.
	if gotReq.TenantId != "t1" || gotReq.Filename != "movie.mp4" || gotReq.SizeBytes != 1000 {
		t.Fatalf("outbound request not built from input: %+v", gotReq)
	}
	session, ok := res.(*model.VodUploadSession)
	if !ok {
		t.Fatalf("expected VodUploadSession, got %T", res)
	}
	if session.ID != "up1" || session.ArtifactHash != "hash1" || session.PlaybackID != "pl1" || session.PartSize != 5242880 {
		t.Fatalf("response not mapped: %+v", session)
	}
}

// S3-not-configured is surfaced as a typed ValidationError union member, not a Go error.
func TestDoCreateVodUploadStorageNotConfigured(t *testing.T) {
	r := commoR(&clientstest.FakeCommodore{
		CreateVodUploadFn: func(context.Context, *sharedpb.CreateVodUploadRequest) (*sharedpb.CreateVodUploadResponse, error) {
			return nil, status.Error(codes.FailedPrecondition, "S3 storage not configured")
		},
	})
	res, err := r.DoCreateVodUpload(clientstest.AuthedCtx("t1"), model.CreateVodUploadInput{Filename: "f", SizeBytes: 1})
	if err != nil {
		t.Fatalf("storage-not-configured should be a typed result: %v", err)
	}
	if _, ok := res.(*model.ValidationError); !ok {
		t.Fatalf("expected ValidationError, got %T", res)
	}

	// No tenant → guard fires before backend.
	guard := commoR(&clientstest.FakeCommodore{})
	if _, err := guard.DoCreateVodUpload(context.Background(), model.CreateVodUploadInput{Filename: "f", SizeBytes: 1}); err == nil {
		t.Fatal("DoCreateVodUpload should require tenant context")
	}
	if guard.Clients.Commodore.(*clientstest.FakeCommodore).Calls != 0 {
		t.Fatal("tenant guard must short-circuit before backend")
	}
}

// DoCompleteVodUpload forwards tenant + upload_id + converted parts, and returns
// the finalized asset.
func TestDoCompleteVodUpload(t *testing.T) {
	var gotReq *sharedpb.CompleteVodUploadRequest
	commo := &clientstest.FakeCommodore{
		CompleteVodUploadFn: func(_ context.Context, req *sharedpb.CompleteVodUploadRequest) (*sharedpb.CompleteVodUploadResponse, error) {
			gotReq = req
			return &sharedpb.CompleteVodUploadResponse{
				Asset: &sharedpb.VodAssetInfo{ArtifactHash: "hash1", Status: sharedpb.VodStatus_VOD_STATUS_PROCESSING},
			}, nil
		},
	}
	r := commoR(commo)
	res, err := r.DoCompleteVodUpload(clientstest.AuthedCtx("t1"), model.CompleteVodUploadInput{
		UploadID: "up1",
		Parts:    []*model.VodUploadCompletedPart{{PartNumber: 1, Etag: "e1"}},
	})
	if err != nil {
		t.Fatalf("DoCompleteVodUpload err: %v", err)
	}
	if gotReq.TenantId != "t1" || gotReq.UploadId != "up1" {
		t.Fatalf("tenant/upload not forwarded: %+v", gotReq)
	}
	if len(gotReq.Parts) != 1 || gotReq.Parts[0].PartNumber != 1 || gotReq.Parts[0].Etag != "e1" {
		t.Fatalf("parts not converted: %+v", gotReq.Parts)
	}
	asset, ok := res.(*model.VodAsset)
	if !ok || asset.ArtifactHash != "hash1" || asset.Status != model.VodAssetStatusProcessing {
		t.Fatalf("expected mapped VodAsset, got %T %+v", res, res)
	}
}

// A NotFound on complete maps to the typed NotFoundError carrying the upload_id.
func TestDoCompleteVodUploadNotFound(t *testing.T) {
	r := commoR(&clientstest.FakeCommodore{
		CompleteVodUploadFn: func(context.Context, *sharedpb.CompleteVodUploadRequest) (*sharedpb.CompleteVodUploadResponse, error) {
			return nil, status.Error(codes.NotFound, "gone")
		},
	})
	res, err := r.DoCompleteVodUpload(clientstest.AuthedCtx("t1"), model.CompleteVodUploadInput{UploadID: "up9"})
	if err != nil {
		t.Fatalf("not-found should be a typed result: %v", err)
	}
	if nf, ok := res.(*model.NotFoundError); !ok || nf.ResourceType != "VodUpload" || nf.ResourceID != "up9" {
		t.Fatalf("expected VodUpload NotFoundError, got %T %+v", res, res)
	}
}

func TestDoAbortVodUpload(t *testing.T) {
	var gotTenant, gotUpload string
	commo := &clientstest.FakeCommodore{
		AbortVodUploadFn: func(_ context.Context, tenantID, uploadID string) (*sharedpb.AbortVodUploadResponse, error) {
			gotTenant, gotUpload = tenantID, uploadID
			return &sharedpb.AbortVodUploadResponse{Success: true}, nil
		},
	}
	r := commoR(commo)
	res, err := r.DoAbortVodUpload(clientstest.AuthedCtx("t1"), "up1")
	if err != nil {
		t.Fatal(err)
	}
	if gotTenant != "t1" || gotUpload != "up1" {
		t.Fatalf("tenant/upload not forwarded: %q %q", gotTenant, gotUpload)
	}
	if ok, isOK := res.(*model.DeleteSuccess); !isOK || ok.DeletedID != "up1" {
		t.Fatalf("expected DeleteSuccess for up1, got %T %+v", res, res)
	}

	// "not found" → typed NotFoundError, no Go error.
	rNF := commoR(&clientstest.FakeCommodore{
		AbortVodUploadFn: func(context.Context, string, string) (*sharedpb.AbortVodUploadResponse, error) {
			return nil, errors.New("upload not found")
		},
	})
	res, err = rNF.DoAbortVodUpload(clientstest.AuthedCtx("t1"), "ghost")
	if err != nil {
		t.Fatalf("not-found should not be a Go error: %v", err)
	}
	if nf, isNF := res.(*model.NotFoundError); !isNF || nf.ResourceType != "VodUpload" || nf.ResourceID != "ghost" {
		t.Fatalf("expected VodUpload NotFoundError, got %T %+v", res, res)
	}
}

// DoGetVodUploadStatus maps the proto status enum + part bookkeeping onto the model.
func TestDoGetVodUploadStatus(t *testing.T) {
	var gotTenant, gotUpload string
	commo := &clientstest.FakeCommodore{
		GetVodUploadStatusFn: func(_ context.Context, tenantID, uploadID string) (*sharedpb.GetVodUploadStatusResponse, error) {
			gotTenant, gotUpload = tenantID, uploadID
			return &sharedpb.GetVodUploadStatusResponse{
				UploadId:     uploadID,
				State:        sharedpb.VodStatus_VOD_STATUS_PROCESSING,
				MissingParts: []int32{2, 4},
				ArtifactHash: "hash1",
			}, nil
		},
	}
	r := commoR(commo)
	res, err := r.DoGetVodUploadStatus(clientstest.AuthedCtx("t1"), "up1")
	if err != nil {
		t.Fatal(err)
	}
	if gotTenant != "t1" || gotUpload != "up1" {
		t.Fatalf("tenant/upload not forwarded: %q %q", gotTenant, gotUpload)
	}
	st, ok := res.(*model.VodUploadStatus)
	if !ok {
		t.Fatalf("expected VodUploadStatus, got %T", res)
	}
	if st.State != model.VodAssetStatusProcessing {
		t.Fatalf("state not mapped: %v", st.State)
	}
	if len(st.MissingParts) != 2 || st.MissingParts[0] != 2 || st.MissingParts[1] != 4 {
		t.Fatalf("missing parts not mapped: %v", st.MissingParts)
	}
	if st.ArtifactHash == nil || *st.ArtifactHash != "hash1" {
		t.Fatalf("artifact hash not mapped: %v", st.ArtifactHash)
	}

	// Empty upload_id short-circuits to a ValidationError BEFORE any backend call.
	guard := commoR(&clientstest.FakeCommodore{})
	res, err = guard.DoGetVodUploadStatus(clientstest.AuthedCtx("t1"), "")
	if err != nil {
		t.Fatalf("empty id should be a typed result: %v", err)
	}
	if _, ok := res.(*model.ValidationError); !ok {
		t.Fatalf("expected ValidationError for empty upload_id, got %T", res)
	}
	if guard.Clients.Commodore.(*clientstest.FakeCommodore).Calls != 0 {
		t.Fatal("empty upload_id must short-circuit before backend")
	}

	// gRPC NotFound classifies to the typed NotFoundError.
	rNF := commoR(&clientstest.FakeCommodore{
		GetVodUploadStatusFn: func(context.Context, string, string) (*sharedpb.GetVodUploadStatusResponse, error) {
			return nil, status.Error(codes.NotFound, "gone")
		},
	})
	res, err = rNF.DoGetVodUploadStatus(clientstest.AuthedCtx("t1"), "up9")
	if err != nil {
		t.Fatalf("not-found should be a typed result: %v", err)
	}
	if nf, ok := res.(*model.NotFoundError); !ok || nf.ResourceID != "up9" {
		t.Fatalf("expected NotFoundError for up9, got %T %+v", res, res)
	}
}

// DoGetVodAsset returns the mapped asset on success and (nil, nil) — not an error
// — when the backend reports the asset is gone.
func TestDoGetVodAsset(t *testing.T) {
	var gotTenant, gotID string
	commo := &clientstest.FakeCommodore{
		GetVodAssetFn: func(_ context.Context, tenantID, artifactHash string) (*sharedpb.VodAssetInfo, error) {
			gotTenant, gotID = tenantID, artifactHash
			return &sharedpb.VodAssetInfo{ArtifactHash: "hash1", Status: sharedpb.VodStatus_VOD_STATUS_READY}, nil
		},
	}
	r := commoR(commo)
	asset, err := r.DoGetVodAsset(clientstest.AuthedCtx("t1"), "hash1")
	if err != nil || asset == nil {
		t.Fatalf("DoGetVodAsset = (%+v, %v)", asset, err)
	}
	if gotTenant != "t1" || gotID != "hash1" {
		t.Fatalf("tenant/id not forwarded: %q %q", gotTenant, gotID)
	}
	if asset.ArtifactHash != "hash1" || asset.Status != model.VodAssetStatusReady {
		t.Fatalf("asset not mapped: %+v", asset)
	}

	// "not found" maps to nil asset, nil error (nullable GraphQL field).
	rNF := commoR(&clientstest.FakeCommodore{
		GetVodAssetFn: func(context.Context, string, string) (*sharedpb.VodAssetInfo, error) {
			return nil, errors.New("asset not found")
		},
	})
	asset, err = rNF.DoGetVodAsset(clientstest.AuthedCtx("t1"), "ghost")
	if err != nil || asset != nil {
		t.Fatalf("not-found should be (nil, nil), got (%+v, %v)", asset, err)
	}

	// Non-not-found backend error surfaces.
	rErr := commoR(&clientstest.FakeCommodore{
		GetVodAssetFn: func(context.Context, string, string) (*sharedpb.VodAssetInfo, error) {
			return nil, errors.New("commodore down")
		},
	})
	if _, err := rErr.DoGetVodAsset(clientstest.AuthedCtx("t1"), "x"); err == nil {
		t.Fatal("non-not-found backend error should surface")
	}

	// No tenant → guard fires before backend.
	guard := commoR(&clientstest.FakeCommodore{})
	if _, err := guard.DoGetVodAsset(context.Background(), "hash1"); err == nil {
		t.Fatal("DoGetVodAsset should require tenant context")
	}
	if guard.Clients.Commodore.(*clientstest.FakeCommodore).Calls != 0 {
		t.Fatal("tenant guard must short-circuit before backend")
	}
}

func TestDoDeleteVodAsset(t *testing.T) {
	var gotTenant, gotID string
	commo := &clientstest.FakeCommodore{
		DeleteVodAssetFn: func(_ context.Context, tenantID, artifactHash string) (*sharedpb.DeleteVodAssetResponse, error) {
			gotTenant, gotID = tenantID, artifactHash
			return &sharedpb.DeleteVodAssetResponse{Success: true}, nil
		},
	}
	r := commoR(commo)
	res, err := r.DoDeleteVodAsset(clientstest.AuthedCtx("t1"), "hash1")
	if err != nil {
		t.Fatal(err)
	}
	if gotTenant != "t1" || gotID != "hash1" {
		t.Fatalf("tenant/id not forwarded: %q %q", gotTenant, gotID)
	}
	if ok, isOK := res.(*model.DeleteSuccess); !isOK || ok.DeletedID != "hash1" {
		t.Fatalf("expected DeleteSuccess for hash1, got %T %+v", res, res)
	}

	// "not found" → typed NotFoundError.
	rNF := commoR(&clientstest.FakeCommodore{
		DeleteVodAssetFn: func(context.Context, string, string) (*sharedpb.DeleteVodAssetResponse, error) {
			return nil, errors.New("asset not found")
		},
	})
	res, err = rNF.DoDeleteVodAsset(clientstest.AuthedCtx("t1"), "ghost")
	if err != nil {
		t.Fatalf("not-found should not be a Go error: %v", err)
	}
	if nf, isNF := res.(*model.NotFoundError); !isNF || nf.ResourceType != "VodAsset" || nf.ResourceID != "ghost" {
		t.Fatalf("expected VodAsset NotFoundError, got %T %+v", res, res)
	}

	// No tenant → guard fires before backend.
	guard := commoR(&clientstest.FakeCommodore{})
	if _, err := guard.DoDeleteVodAsset(context.Background(), "hash1"); err == nil {
		t.Fatal("DoDeleteVodAsset should require tenant context")
	}
	if guard.Clients.Commodore.(*clientstest.FakeCommodore).Calls != 0 {
		t.Fatal("tenant guard must short-circuit before backend")
	}
}
