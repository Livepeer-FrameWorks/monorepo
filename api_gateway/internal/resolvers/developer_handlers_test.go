package resolvers

import (
	"context"
	"errors"
	"testing"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients/clientstest"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// DoCreateDeveloperToken splits a comma-separated permissions string, converts
// expiresIn (days) into an absolute timestamp, and surfaces the one-time
// token value on the response.
func TestDoCreateDeveloperToken(t *testing.T) {
	var got *commodorepb.CreateAPITokenRequest
	c := &clientstest.FakeCommodore{
		CreateAPITokenFn: func(_ context.Context, req *commodorepb.CreateAPITokenRequest) (*commodorepb.CreateAPITokenResponse, error) {
			got = req
			return &commodorepb.CreateAPITokenResponse{
				Id:          "tok1",
				TokenValue:  "fwk_secret",
				TokenName:   req.TokenName,
				Permissions: req.Permissions,
				ExpiresAt:   req.ExpiresAt,
			}, nil
		},
	}
	perms := "streams:read, streams:write"
	expiresIn := 7
	out, err := commoW2(c).DoCreateDeveloperToken(clientstest.AuthedCtx("t1"), model.CreateDeveloperTokenInput{
		Name:        "ci-token",
		Permissions: &perms,
		ExpiresIn:   &expiresIn,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.TokenName != "ci-token" {
		t.Fatalf("TokenName = %q", got.TokenName)
	}
	if len(got.Permissions) != 2 || got.Permissions[0] != "streams:read" || got.Permissions[1] != "streams:write" {
		t.Fatalf("permissions not split/trimmed: %+v", got.Permissions)
	}
	if got.ExpiresAt == nil {
		t.Fatal("ExpiresAt should be set from expiresIn")
	}
	// 7 days out (allow a wide tolerance for execution time).
	if d := time.Until(got.ExpiresAt.AsTime()); d < 6*24*time.Hour || d > 8*24*time.Hour {
		t.Fatalf("ExpiresAt ~7d expected, got %v", d)
	}
	if out.GetTokenValue() != "fwk_secret" || out.Id != "tok1" || out.Status != "active" {
		t.Fatalf("unexpected output: %+v", out)
	}

	fail := commoW2(&clientstest.FakeCommodore{
		CreateAPITokenFn: func(context.Context, *commodorepb.CreateAPITokenRequest) (*commodorepb.CreateAPITokenResponse, error) {
			return nil, errors.New("nope")
		},
	})
	if _, err := fail.DoCreateDeveloperToken(clientstest.AuthedCtx("t1"), model.CreateDeveloperTokenInput{Name: "x"}); err == nil {
		t.Fatal("backend error should propagate")
	}
}

// DoRevokeDeveloperToken returns DeleteSuccess on success and classifies a
// "not found" backend error into a typed NotFoundError union member.
func TestDoRevokeDeveloperToken(t *testing.T) {
	var gotID string
	c := &clientstest.FakeCommodore{
		RevokeAPITokenFn: func(_ context.Context, id string) (*commodorepb.RevokeAPITokenResponse, error) {
			gotID = id
			return &commodorepb.RevokeAPITokenResponse{TokenId: id}, nil
		},
	}
	res, err := commoW2(c).DoRevokeDeveloperToken(clientstest.AuthedCtx("t1"), "tok9")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if gotID != "tok9" {
		t.Fatalf("id = %q, want tok9", gotID)
	}
	ok, isOK := res.(*model.DeleteSuccess)
	if !isOK || !ok.Success || ok.DeletedID != "tok9" {
		t.Fatalf("expected DeleteSuccess, got %T %+v", res, res)
	}

	// not-found → NotFoundError union member (no Go error).
	nf := commoW2(&clientstest.FakeCommodore{
		RevokeAPITokenFn: func(context.Context, string) (*commodorepb.RevokeAPITokenResponse, error) {
			return nil, errors.New("token not found")
		},
	})
	res2, err := nf.DoRevokeDeveloperToken(clientstest.AuthedCtx("t1"), "tok9")
	if err != nil {
		t.Fatalf("not-found should be a union member, got err %v", err)
	}
	if nfe, ok := res2.(*model.NotFoundError); !ok || nfe.ResourceID != "tok9" {
		t.Fatalf("expected NotFoundError, got %T %+v", res2, res2)
	}

	// Any other backend error is a hard error.
	fail := commoW2(&clientstest.FakeCommodore{
		RevokeAPITokenFn: func(context.Context, string) (*commodorepb.RevokeAPITokenResponse, error) {
			return nil, errors.New("network down")
		},
	})
	if _, err := fail.DoRevokeDeveloperToken(clientstest.AuthedCtx("t1"), "tok9"); err == nil {
		t.Fatal("non-notfound backend error should propagate")
	}
}

// DoGetDeveloperTokens lists tokens (passing nil pagination) and returns the
// proto slice verbatim.
func TestDoGetDeveloperTokens(t *testing.T) {
	sawNilPagination := false
	c := &clientstest.FakeCommodore{
		ListAPITokensFn: func(_ context.Context, p *commonpb.CursorPaginationRequest) (*commodorepb.ListAPITokensResponse, error) {
			sawNilPagination = p == nil
			return &commodorepb.ListAPITokensResponse{Tokens: []*commodorepb.APITokenInfo{
				{Id: "t1"}, {Id: "t2"},
			}}, nil
		},
	}
	out, err := commoW2(c).DoGetDeveloperTokens(clientstest.AuthedCtx("t1"))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !sawNilPagination {
		t.Fatal("DoGetDeveloperTokens should pass nil pagination")
	}
	if len(out) != 2 || out[0].Id != "t1" {
		t.Fatalf("unexpected tokens: %+v", out)
	}

	fail := commoW2(&clientstest.FakeCommodore{
		ListAPITokensFn: func(context.Context, *commonpb.CursorPaginationRequest) (*commodorepb.ListAPITokensResponse, error) {
			return nil, errors.New("down")
		},
	})
	if _, err := fail.DoGetDeveloperTokens(clientstest.AuthedCtx("t1")); err == nil {
		t.Fatal("backend error should propagate")
	}
}

// DoGetDeveloperTokensConnection forwards a forward-pagination request (first)
// and maps the proto response into a Relay connection with cursors derived
// from each token's CreatedAt + Id.
func TestDoGetDeveloperTokensConnection(t *testing.T) {
	now := time.Now()
	endCur := "cur-end"
	var gotReq *commonpb.CursorPaginationRequest
	c := &clientstest.FakeCommodore{
		ListAPITokensFn: func(_ context.Context, p *commonpb.CursorPaginationRequest) (*commodorepb.ListAPITokensResponse, error) {
			gotReq = p
			return &commodorepb.ListAPITokensResponse{
				Tokens: []*commodorepb.APITokenInfo{
					{Id: "t1", CreatedAt: timestamppb.New(now)},
				},
				Pagination: &commonpb.CursorPaginationResponse{
					HasNextPage: true,
					EndCursor:   &endCur,
					TotalCount:  5,
				},
			}, nil
		},
	}
	first := 10
	conn, err := commoW2(c).DoGetDeveloperTokensConnection(clientstest.AuthedCtx("t1"), &first, nil, nil, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if gotReq == nil || gotReq.First != 10 {
		t.Fatalf("forward pagination request First = %+v, want 10", gotReq)
	}
	if len(conn.Edges) != 1 || conn.Edges[0].Node.Id != "t1" || conn.Edges[0].Cursor == "" {
		t.Fatalf("edges mapped wrong: %+v", conn.Edges)
	}
	if !conn.PageInfo.HasNextPage || conn.PageInfo.EndCursor == nil || *conn.PageInfo.EndCursor != "cur-end" {
		t.Fatalf("pageInfo mapped wrong: %+v", conn.PageInfo)
	}
	if conn.TotalCount != 5 {
		t.Fatalf("TotalCount = %d, want 5", conn.TotalCount)
	}

	fail := commoW2(&clientstest.FakeCommodore{
		ListAPITokensFn: func(context.Context, *commonpb.CursorPaginationRequest) (*commodorepb.ListAPITokensResponse, error) {
			return nil, errors.New("down")
		},
	})
	if _, err := fail.DoGetDeveloperTokensConnection(clientstest.AuthedCtx("t1"), &first, nil, nil, nil); err == nil {
		t.Fatal("backend error should propagate")
	}
}
