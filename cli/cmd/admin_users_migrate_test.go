package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
	"google.golang.org/grpc"
)

type fakeAdminUsersClient struct {
	req  *commodorepb.CreateUserInTenantRequest
	resp *commodorepb.CreateUserInTenantResponse
	err  error
}

func (f *fakeAdminUsersClient) CreateUserInTenant(_ context.Context, req *commodorepb.CreateUserInTenantRequest) (*commodorepb.CreateUserInTenantResponse, error) {
	f.req = req
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func TestRunUserCreate(t *testing.T) {
	fake := &fakeAdminUsersClient{resp: &commodorepb.CreateUserInTenantResponse{
		User: &commodorepb.User{Id: "u-1", Email: bootstrapStrPtr("a@b.com"), Role: "owner"},
	}}
	req := &commodorepb.CreateUserInTenantRequest{Email: "a@b.com"}
	var buf bytes.Buffer
	if err := runUserCreate(context.Background(), &buf, fake, "", "tenant-1", req, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.req != req {
		t.Error("request not forwarded unchanged")
	}
	out := buf.String()
	// "tenant" field is the passed tenantID, not a response field.
	for _, want := range []string{"a@b.com", "u-1", "tenant-1", "owner", "login --email a@b.com"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}

	var jbuf bytes.Buffer
	if err := runUserCreate(context.Background(), &jbuf, fake, "", "tenant-1", req, true); err != nil {
		t.Fatalf("json: %v", err)
	}
	if !json.Valid(jbuf.Bytes()) {
		t.Errorf("not valid JSON: %s", jbuf.String())
	}

	errFake := &fakeAdminUsersClient{err: errors.New("boom")}
	if err := runUserCreate(context.Background(), &bytes.Buffer{}, errFake, "", "tenant-1", req, false); err == nil {
		t.Fatal("expected create error to propagate")
	}
}

type fakeArtifactMigrator struct {
	req  *foghornfederationpb.MigrateArtifactMetadataRequest
	resp *foghornfederationpb.MigrateArtifactMetadataResponse
	err  error
}

func (f *fakeArtifactMigrator) MigrateArtifactMetadata(_ context.Context, in *foghornfederationpb.MigrateArtifactMetadataRequest, _ ...grpc.CallOption) (*foghornfederationpb.MigrateArtifactMetadataResponse, error) {
	f.req = in
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func TestRunMigrateArtifacts_Success(t *testing.T) {
	fake := &fakeArtifactMigrator{resp: &foghornfederationpb.MigrateArtifactMetadataResponse{MigratedCount: 7, AlreadyExists: 2}}
	req := &foghornfederationpb.MigrateArtifactMetadataRequest{TenantId: "t-1", SourceClusterId: "c-1"}
	var buf bytes.Buffer
	if err := runMigrateArtifacts(context.Background(), &buf, fake, "", req, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.req != req {
		t.Error("request not forwarded unchanged")
	}
	out := buf.String()
	for _, want := range []string{"Artifact metadata migration complete", "migrated", "7", "already existed", "2"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}

	var jbuf bytes.Buffer
	if err := runMigrateArtifacts(context.Background(), &jbuf, fake, "", req, true); err != nil {
		t.Fatalf("json: %v", err)
	}
	if !json.Valid(jbuf.Bytes()) {
		t.Errorf("not valid JSON: %s", jbuf.String())
	}
}

func TestRunMigrateArtifacts_RPCError(t *testing.T) {
	fake := &fakeArtifactMigrator{err: errors.New("transport down")}
	if err := runMigrateArtifacts(context.Background(), &bytes.Buffer{}, fake, "", &foghornfederationpb.MigrateArtifactMetadataRequest{}, false); err == nil {
		t.Fatal("expected RPC error to propagate")
	}
}

func TestRunMigrateArtifacts_ApplicationError(t *testing.T) {
	// A non-empty resp.Error is a failed migration even though the RPC succeeded.
	fake := &fakeArtifactMigrator{resp: &foghornfederationpb.MigrateArtifactMetadataResponse{Error: "source unreachable"}}
	err := runMigrateArtifacts(context.Background(), &bytes.Buffer{}, fake, "", &foghornfederationpb.MigrateArtifactMetadataRequest{}, false)
	if err == nil || !strings.Contains(err.Error(), "source unreachable") {
		t.Fatalf("expected application error surfaced, got %v", err)
	}
}
