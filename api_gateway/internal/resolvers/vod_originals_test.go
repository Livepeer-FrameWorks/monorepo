package resolvers

import (
	"context"
	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients/clientstest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/globalid"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	"testing"
	"time"
)

func TestOriginalDownloadRequiresTenantAndLimitsGrants(t *testing.T) {
	calls := 0
	client := &clientstest.FakeCommodore{CreateVodOriginalDownloadFn: func(_ context.Context, req *sharedpb.CreateVodOriginalDownloadRequest) (*sharedpb.CreateVodOriginalDownloadResponse, error) {
		calls++
		if req.GetArtifactHash() != "asset" {
			t.Fatalf("wrong asset: %+v", req)
		}
		return &sharedpb.CreateVodOriginalDownloadResponse{Url: "https://store/original", ExpiresAt: timestamppb.New(time.Now().Add(time.Minute)), SizeBytes: 123, Filename: "source.mov"}, nil
	}}
	r := commoR(client)
	if _, err := r.DoCreateVodOriginalDownload(context.Background(), "asset"); err == nil {
		t.Fatal("anonymous original download accepted")
	}
	ctx := clientstest.AuthedCtx("original-grants-limit-test-" + time.Now().Format(time.RFC3339Nano))
	id := globalid.Encode(globalid.TypeVodAsset, "asset")
	for i := 0; i < originalDownloadBurst+1; i++ {
		result, err := r.DoCreateVodOriginalDownload(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if i < originalDownloadBurst {
			if grant, ok := result.(*model.VodOriginalDownload); !ok || grant.SizeBytes != 123 || grant.Filename != "source.mov" {
				t.Fatalf("wrong grant %T: %+v", result, result)
			}
		} else {
			if _, ok := result.(*model.ValidationError); !ok {
				t.Fatalf("burst exceeded but got %T", result)
			}
		}
	}
	if calls != originalDownloadBurst {
		t.Fatalf("issued %d grants, want %d", calls, originalDownloadBurst)
	}
}

func TestOriginalDownloadShowsSharedTenantLimit(t *testing.T) {
	client := &clientstest.FakeCommodore{CreateVodOriginalDownloadFn: func(context.Context, *sharedpb.CreateVodOriginalDownloadRequest) (*sharedpb.CreateVodOriginalDownloadResponse, error) {
		return nil, status.Error(codes.ResourceExhausted, "Too many original download requests; try again shortly")
	}}
	result, err := commoR(client).DoCreateVodOriginalDownload(clientstest.AuthedCtx("original-shared-limit"), globalid.Encode(globalid.TypeVodAsset, "asset"))
	if err != nil {
		t.Fatal(err)
	}
	validation, ok := result.(*model.ValidationError)
	if !ok || validation.Message != "Too many original download requests; try again shortly" {
		t.Fatalf("shared tenant limit returned %T: %+v", result, result)
	}
}

func TestDeleteOriginalDoesNotReportAnAbsentOriginalAsDeleted(t *testing.T) {
	client := &clientstest.FakeCommodore{DeleteVodOriginalFn: func(context.Context, *sharedpb.DeleteVodOriginalRequest) (*sharedpb.DeleteVodOriginalResponse, error) {
		return &sharedpb.DeleteVodOriginalResponse{Success: false}, nil
	}}
	result, err := commoR(client).DoDeleteVodOriginal(clientstest.AuthedCtx("original-delete-noop"), globalid.Encode(globalid.TypeVodAsset, "asset"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.(*model.ValidationError); !ok {
		t.Fatalf("absent original reported as deleted: %T %+v", result, result)
	}
}

func TestOriginalDownloadExplainsMissingOriginal(t *testing.T) {
	client := &clientstest.FakeCommodore{CreateVodOriginalDownloadFn: func(context.Context, *sharedpb.CreateVodOriginalDownloadRequest) (*sharedpb.CreateVodOriginalDownloadResponse, error) {
		return nil, status.Error(codes.FailedPrecondition, "original_not_kept")
	}}
	result, err := commoR(client).DoCreateVodOriginalDownload(clientstest.AuthedCtx("original-readable-error"), globalid.Encode(globalid.TypeVodAsset, "asset"))
	if err != nil {
		t.Fatal(err)
	}
	validation, ok := result.(*model.ValidationError)
	if !ok || validation.Message != "No original upload is retained for this video" {
		t.Fatalf("raw original disposition shown to user: %+v", result)
	}
}
