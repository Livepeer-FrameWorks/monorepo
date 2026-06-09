package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
)

// fakeAdminTokensClient is a hand-written stand-in for the Commodore gRPC
// client: it records the requests it receives and returns canned responses or
// errors. This is what the *ClientFromContext seam buys — the token-command
// logic runs in-process without dialing a real server.
type fakeAdminTokensClient struct {
	createReq  *commodorepb.CreateAPITokenRequest
	createResp *commodorepb.CreateAPITokenResponse
	createErr  error

	listResp  *commodorepb.ListAPITokensResponse
	listErr   error
	listCalls int

	revokedID   string
	revokeCalls int
	revokeErr   error
}

func (f *fakeAdminTokensClient) CreateAPIToken(_ context.Context, req *commodorepb.CreateAPITokenRequest) (*commodorepb.CreateAPITokenResponse, error) {
	f.createReq = req
	if f.createErr != nil {
		return nil, f.createErr
	}
	return f.createResp, nil
}

func (f *fakeAdminTokensClient) ListAPITokens(_ context.Context, _ *commonpb.CursorPaginationRequest) (*commodorepb.ListAPITokensResponse, error) {
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.listResp, nil
}

func (f *fakeAdminTokensClient) RevokeAPIToken(_ context.Context, tokenID string) (*commodorepb.RevokeAPITokenResponse, error) {
	f.revokeCalls++
	f.revokedID = tokenID
	if f.revokeErr != nil {
		return nil, f.revokeErr
	}
	return &commodorepb.RevokeAPITokenResponse{}, nil
}

func TestRunTokensCreate_BuildsRequestAndRenders(t *testing.T) {
	fake := &fakeAdminTokensClient{
		createResp: &commodorepb.CreateAPITokenResponse{Id: "id-1", TokenName: "ci", TokenValue: "secret-value"},
	}
	var buf bytes.Buffer
	err := runTokensCreate(context.Background(), &buf, fake, "ci", "24h", "read,write", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if fake.createReq.TokenName != "ci" {
		t.Errorf("TokenName = %q, want ci", fake.createReq.TokenName)
	}
	if got := fake.createReq.Permissions; len(got) != 2 || got[0] != "read" || got[1] != "write" {
		t.Errorf("Permissions = %v, want [read write]", got)
	}
	if fake.createReq.ExpiresAt == nil {
		t.Error("ExpiresAt should be set when --expires given")
	}

	out := buf.String()
	if !strings.Contains(out, "Created token") || !strings.Contains(out, "secret-value") {
		t.Errorf("output missing token confirmation/value: %q", out)
	}
}

func TestRunTokensCreate_NoExpiryNoPerms(t *testing.T) {
	fake := &fakeAdminTokensClient{createResp: &commodorepb.CreateAPITokenResponse{Id: "id-1", TokenName: "ci"}}
	var buf bytes.Buffer
	if err := runTokensCreate(context.Background(), &buf, fake, "ci", "", "", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.createReq.Permissions != nil {
		t.Errorf("Permissions = %v, want nil", fake.createReq.Permissions)
	}
	if fake.createReq.ExpiresAt != nil {
		t.Error("ExpiresAt should be nil with no --expires")
	}
}

func TestRunTokensCreate_JSON(t *testing.T) {
	fake := &fakeAdminTokensClient{createResp: &commodorepb.CreateAPITokenResponse{Id: "id-1", TokenName: "ci", TokenValue: "v"}}
	var buf bytes.Buffer
	if err := runTokensCreate(context.Background(), &buf, fake, "ci", "", "", true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	if decoded["id"] != "id-1" {
		t.Errorf("json id = %v, want id-1", decoded["id"])
	}
}

func TestRunTokensCreate_Error(t *testing.T) {
	fake := &fakeAdminTokensClient{createErr: errors.New("boom")}
	var buf bytes.Buffer
	if err := runTokensCreate(context.Background(), &buf, fake, "ci", "", "", false); err == nil {
		t.Fatal("expected error to propagate")
	}
}

func TestRunTokensList_Renders(t *testing.T) {
	fake := &fakeAdminTokensClient{listResp: &commodorepb.ListAPITokensResponse{
		Tokens: []*commodorepb.APITokenInfo{
			{Id: "a", TokenName: "first", Status: "active"},
			{Id: "b", TokenName: "second", Status: "revoked"},
		},
	}}
	var buf bytes.Buffer
	if err := runTokensList(context.Background(), &buf, fake, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"Tokens (2)", "first", "second", "active", "revoked"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q: %q", want, out)
		}
	}
}

func TestRunTokensList_JSON(t *testing.T) {
	fake := &fakeAdminTokensClient{listResp: &commodorepb.ListAPITokensResponse{
		Tokens: []*commodorepb.APITokenInfo{{Id: "a", TokenName: "first", Status: "active"}},
	}}
	var buf bytes.Buffer
	if err := runTokensList(context.Background(), &buf, fake, true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	if _, ok := decoded["tokens"]; !ok {
		t.Errorf("json missing tokens field: %s", buf.String())
	}
}

func TestRunTokensList_Error(t *testing.T) {
	fake := &fakeAdminTokensClient{listErr: errors.New("rpc down")}
	var buf bytes.Buffer
	if err := runTokensList(context.Background(), &buf, fake, false); err == nil {
		t.Fatal("expected list error to propagate")
	}
}

const validTokenUUID = "123e4567-e89b-12d3-a456-426614174000"

func confirmYes(string) bool { return true }
func confirmNo(string) bool  { return false }

func TestRunTokensRevoke_ByID(t *testing.T) {
	fake := &fakeAdminTokensClient{}
	var buf bytes.Buffer
	if err := runTokensRevoke(context.Background(), &buf, fake, validTokenUUID, "", confirmYes); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.revokeCalls != 1 || fake.revokedID != validTokenUUID {
		t.Errorf("revoke calls=%d id=%q, want 1 / %s", fake.revokeCalls, fake.revokedID, validTokenUUID)
	}
	if fake.listCalls != 0 {
		t.Errorf("list should not be called when ID is given directly, got %d", fake.listCalls)
	}
	if !strings.Contains(buf.String(), "Revoked token") {
		t.Errorf("missing success message: %q", buf.String())
	}
}

func TestRunTokensRevoke_ByID_InvalidUUID(t *testing.T) {
	fake := &fakeAdminTokensClient{}
	var buf bytes.Buffer
	if err := runTokensRevoke(context.Background(), &buf, fake, "not-a-uuid", "", confirmYes); err == nil {
		t.Fatal("expected error for malformed token ID")
	}
	if fake.revokeCalls != 0 {
		t.Error("must not revoke when ID is invalid")
	}
}

func TestRunTokensRevoke_ByName(t *testing.T) {
	fake := &fakeAdminTokensClient{listResp: &commodorepb.ListAPITokensResponse{
		Tokens: []*commodorepb.APITokenInfo{
			{Id: "other", TokenName: "nope"},
			{Id: "target", TokenName: "ci"},
		},
	}}
	var buf bytes.Buffer
	if err := runTokensRevoke(context.Background(), &buf, fake, "", "ci", confirmYes); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.revokedID != "target" {
		t.Errorf("revoked id = %q, want target (resolved by name)", fake.revokedID)
	}
}

func TestRunTokensRevoke_ByName_NotFound(t *testing.T) {
	fake := &fakeAdminTokensClient{listResp: &commodorepb.ListAPITokensResponse{
		Tokens: []*commodorepb.APITokenInfo{{Id: "x", TokenName: "other"}},
	}}
	var buf bytes.Buffer
	if err := runTokensRevoke(context.Background(), &buf, fake, "", "ci", confirmYes); err == nil {
		t.Fatal("expected not-found error")
	}
	if fake.revokeCalls != 0 {
		t.Error("must not revoke when name is unresolved")
	}
}

func TestRunTokensRevoke_NoArgs(t *testing.T) {
	fake := &fakeAdminTokensClient{}
	var buf bytes.Buffer
	if err := runTokensRevoke(context.Background(), &buf, fake, "", "", confirmYes); err == nil {
		t.Fatal("expected error when neither ID nor --name supplied")
	}
}

func TestRunTokensRevoke_ConfirmFalse(t *testing.T) {
	fake := &fakeAdminTokensClient{}
	var buf bytes.Buffer
	if err := runTokensRevoke(context.Background(), &buf, fake, validTokenUUID, "", confirmNo); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.revokeCalls != 0 {
		t.Error("declining confirmation must not revoke")
	}
	if !strings.Contains(buf.String(), "Cancelled") {
		t.Errorf("expected Cancelled message: %q", buf.String())
	}
}

func TestRunTokensRevoke_RevokeError(t *testing.T) {
	fake := &fakeAdminTokensClient{revokeErr: errors.New("rpc rejected")}
	var buf bytes.Buffer
	if err := runTokensRevoke(context.Background(), &buf, fake, validTokenUUID, "", confirmYes); err == nil {
		t.Fatal("expected revoke error to propagate")
	}
}
