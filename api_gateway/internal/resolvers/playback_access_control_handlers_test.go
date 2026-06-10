package resolvers

import (
	"context"
	"errors"
	"testing"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients/clientstest"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// DoCreateSigningKey forwards the trimmed name and returns the key plus its
// one-time private PEM; an empty name is a local ValidationError.
func TestDoCreateSigningKey(t *testing.T) {
	var gotName string
	c := &clientstest.FakeCommodore{
		CreateSigningKeyFn: func(_ context.Context, name string) (*commodorepb.CreateSigningKeyResponse, error) {
			gotName = name
			return &commodorepb.CreateSigningKeyResponse{
				SigningKey:    &commodorepb.SigningKey{Id: "sk1", Name: name},
				PrivateKeyPem: "PEM",
			}, nil
		},
	}
	res, err := commoW2(c).DoCreateSigningKey(clientstest.AuthedCtx("t1"), model.CreateSigningKeyInput{Name: "  staging  "})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if gotName != "staging" {
		t.Fatalf("name not trimmed: %q", gotName)
	}
	ok, isOK := res.(*model.CreateSigningKeySuccess)
	if !isOK || ok.PrivateKeyPem != "PEM" || ok.SigningKey.Id != "sk1" {
		t.Fatalf("expected success, got %T %+v", res, res)
	}

	// Empty name → ValidationError, no backend call.
	bad := &clientstest.FakeCommodore{}
	res, _ = commoW2(bad).DoCreateSigningKey(clientstest.AuthedCtx("t1"), model.CreateSigningKeyInput{Name: "   "})
	if _, ok := res.(*model.ValidationError); !ok {
		t.Fatalf("expected ValidationError, got %T", res)
	}
	if bad.Calls != 0 {
		t.Fatalf("validation must not reach backend, Calls=%d", bad.Calls)
	}

	denied := &clientstest.FakeCommodore{}
	if _, derr := commoW2(denied).DoCreateSigningKey(context.Background(), model.CreateSigningKeyInput{Name: "x"}); derr == nil {
		t.Fatal("expected permission error")
	}
	if denied.Calls != 0 {
		t.Fatalf("guard must not reach backend, Calls=%d", denied.Calls)
	}

	// InvalidArgument is mapped into a ValidationError union member.
	inv := commoW2(&clientstest.FakeCommodore{
		CreateSigningKeyFn: func(context.Context, string) (*commodorepb.CreateSigningKeyResponse, error) {
			return nil, status.Error(codes.InvalidArgument, "bad name")
		},
	})
	res, err = inv.DoCreateSigningKey(clientstest.AuthedCtx("t1"), model.CreateSigningKeyInput{Name: "x"})
	if err != nil {
		t.Fatalf("InvalidArgument should be a union member: %v", err)
	}
	if _, ok := res.(*model.ValidationError); !ok {
		t.Fatalf("expected ValidationError, got %T", res)
	}
}

// DoRevokeSigningKey forwards the trimmed id and returns the revoked key;
// empty id short-circuits to NotFoundError; NotFound from Commodore maps to
// a NotFoundError union member.
func TestDoRevokeSigningKey(t *testing.T) {
	var gotID string
	c := &clientstest.FakeCommodore{
		RevokeSigningKeyFn: func(_ context.Context, id string) (*commodorepb.SigningKey, error) {
			gotID = id
			return &commodorepb.SigningKey{Id: id, Status: "revoked"}, nil
		},
	}
	res, err := commoW2(c).DoRevokeSigningKey(clientstest.AuthedCtx("t1"), "sk9")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if gotID != "sk9" {
		t.Fatalf("id = %q, want sk9", gotID)
	}
	if sk, ok := res.(*commodorepb.SigningKey); !ok || sk.Status != "revoked" {
		t.Fatalf("expected revoked key, got %T %+v", res, res)
	}

	// Empty id → NotFoundError, no backend call.
	bad := &clientstest.FakeCommodore{}
	res, _ = commoW2(bad).DoRevokeSigningKey(clientstest.AuthedCtx("t1"), "  ")
	if _, ok := res.(*model.NotFoundError); !ok {
		t.Fatalf("expected NotFoundError, got %T", res)
	}
	if bad.Calls != 0 {
		t.Fatalf("empty id must not reach backend, Calls=%d", bad.Calls)
	}

	nf := commoW2(&clientstest.FakeCommodore{
		RevokeSigningKeyFn: func(context.Context, string) (*commodorepb.SigningKey, error) {
			return nil, status.Error(codes.NotFound, "missing")
		},
	})
	res, err = nf.DoRevokeSigningKey(clientstest.AuthedCtx("t1"), "sk9")
	if err != nil {
		t.Fatalf("NotFound should be a union member: %v", err)
	}
	if _, ok := res.(*model.NotFoundError); !ok {
		t.Fatalf("expected NotFoundError, got %T", res)
	}
}

// DoSetPlaybackPolicy builds the request from the chosen target + policy type,
// then resolves the updated object back. With a stream target it refetches the
// stream via Commodore.GetStream.
func TestDoSetPlaybackPolicy(t *testing.T) {
	var got *commodorepb.SetPlaybackPolicyRequest
	var refetched string
	c := &clientstest.FakeCommodore{
		SetPlaybackPolicyFn: func(_ context.Context, req *commodorepb.SetPlaybackPolicyRequest) (*commodorepb.SetPlaybackPolicyResponse, error) {
			got = req
			return &commodorepb.SetPlaybackPolicyResponse{StreamId: req.StreamId}, nil
		},
		GetStreamFn: func(_ context.Context, id string) (*commodorepb.Stream, error) {
			refetched = id
			return &commodorepb.Stream{StreamId: id}, nil
		},
	}
	streamID := "s1"
	res, err := commoW2(c).DoSetPlaybackPolicy(clientstest.AuthedCtx("t1"), model.SetPlaybackPolicyInput{
		StreamID: &streamID,
		Policy:   &model.PlaybackPolicyInput{Type: model.PlaybackPolicyTypePublic},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.StreamId != "s1" || got.Type != "public" {
		t.Fatalf("request built wrong: %+v", got)
	}
	if refetched != "s1" {
		t.Fatalf("stream not refetched: %q", refetched)
	}
	if st, ok := res.(*commodorepb.Stream); !ok || st.StreamId != "s1" {
		t.Fatalf("expected refetched stream, got %T %+v", res, res)
	}

	// nil policy → ValidationError, no backend call.
	bad := &clientstest.FakeCommodore{}
	res, _ = commoW2(bad).DoSetPlaybackPolicy(clientstest.AuthedCtx("t1"), model.SetPlaybackPolicyInput{StreamID: &streamID})
	if _, ok := res.(*model.ValidationError); !ok {
		t.Fatalf("expected ValidationError for nil policy, got %T", res)
	}
	if bad.Calls != 0 {
		t.Fatalf("validation must not reach backend, Calls=%d", bad.Calls)
	}

	// JWT type without a jwt block → ValidationError.
	res, _ = commoW2(&clientstest.FakeCommodore{}).DoSetPlaybackPolicy(clientstest.AuthedCtx("t1"), model.SetPlaybackPolicyInput{
		StreamID: &streamID,
		Policy:   &model.PlaybackPolicyInput{Type: model.PlaybackPolicyTypeJwt},
	})
	if _, ok := res.(*model.ValidationError); !ok {
		t.Fatalf("expected ValidationError for missing jwt block, got %T", res)
	}

	denied := &clientstest.FakeCommodore{}
	if _, err := commoW2(denied).DoSetPlaybackPolicy(context.Background(), model.SetPlaybackPolicyInput{StreamID: &streamID, Policy: &model.PlaybackPolicyInput{Type: model.PlaybackPolicyTypePublic}}); err == nil {
		t.Fatal("expected permission error")
	}
	if denied.Calls != 0 {
		t.Fatalf("guard must not reach backend, Calls=%d", denied.Calls)
	}
}

// DoGetSigningKey forwards the trimmed id; empty id and a NotFound backend
// error both resolve to (nil, nil) rather than an error.
func TestDoGetSigningKey(t *testing.T) {
	c := &clientstest.FakeCommodore{
		GetSigningKeyFn: func(_ context.Context, id string) (*commodorepb.SigningKey, error) {
			return &commodorepb.SigningKey{Id: id}, nil
		},
	}
	sk, err := commoW2(c).DoGetSigningKey(clientstest.AuthedCtx("t1"), "sk1")
	if err != nil || sk == nil || sk.Id != "sk1" {
		t.Fatalf("DoGetSigningKey = (%+v, %v)", sk, err)
	}

	// Empty id short-circuits to nil without a backend call.
	empty := &clientstest.FakeCommodore{}
	sk, err = commoW2(empty).DoGetSigningKey(clientstest.AuthedCtx("t1"), "  ")
	if err != nil || sk != nil {
		t.Fatalf("empty id should be (nil,nil), got (%+v,%v)", sk, err)
	}
	if empty.Calls != 0 {
		t.Fatalf("empty id must not reach backend, Calls=%d", empty.Calls)
	}

	// NotFound → (nil, nil).
	nf := commoW2(&clientstest.FakeCommodore{
		GetSigningKeyFn: func(context.Context, string) (*commodorepb.SigningKey, error) {
			return nil, status.Error(codes.NotFound, "missing")
		},
	})
	sk, err = nf.DoGetSigningKey(clientstest.AuthedCtx("t1"), "sk1")
	if err != nil || sk != nil {
		t.Fatalf("NotFound should be (nil,nil), got (%+v,%v)", sk, err)
	}

	// Other errors propagate.
	fail := commoW2(&clientstest.FakeCommodore{
		GetSigningKeyFn: func(context.Context, string) (*commodorepb.SigningKey, error) {
			return nil, errors.New("down")
		},
	})
	if _, err := fail.DoGetSigningKey(clientstest.AuthedCtx("t1"), "sk1"); err == nil {
		t.Fatal("non-notfound error should propagate")
	}
}

// DoListSigningKeys forwards the status filter + clamped limit + after cursor
// and maps each key into an edge; nextAfterId drives HasNextPage/EndCursor.
func TestDoListSigningKeys(t *testing.T) {
	var gotFilter, gotAfter string
	var gotLimit int32
	c := &clientstest.FakeCommodore{
		ListSigningKeysFn: func(_ context.Context, sf string, limit int32, after string) (*commodorepb.ListSigningKeysResponse, error) {
			gotFilter, gotLimit, gotAfter = sf, limit, after
			return &commodorepb.ListSigningKeysResponse{
				SigningKeys: []*commodorepb.SigningKey{{Id: "sk1"}, {Id: "sk2"}},
				NextAfterId: "sk2",
			}, nil
		},
	}
	first := 25
	after := "sk0"
	sf := "active"
	conn, err := commoW2(c).DoListSigningKeys(clientstest.AuthedCtx("t1"), &sf, &model.ConnectionInput{First: &first, After: &after})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if gotFilter != "active" || gotLimit != 25 || gotAfter != "sk0" {
		t.Fatalf("args forwarded wrong: filter=%q limit=%d after=%q", gotFilter, gotLimit, gotAfter)
	}
	if len(conn.Edges) != 2 || conn.Edges[0].Node.Id != "sk1" || conn.Edges[0].Cursor != "sk1" {
		t.Fatalf("edges mapped wrong: %+v", conn.Edges)
	}
	if !conn.PageInfo.HasNextPage || conn.PageInfo.EndCursor == nil || *conn.PageInfo.EndCursor != "sk2" {
		t.Fatalf("pageInfo mapped wrong: %+v", conn.PageInfo)
	}

	fail := commoW2(&clientstest.FakeCommodore{
		ListSigningKeysFn: func(context.Context, string, int32, string) (*commodorepb.ListSigningKeysResponse, error) {
			return nil, errors.New("down")
		},
	})
	if _, err := fail.DoListSigningKeys(clientstest.AuthedCtx("t1"), nil, nil); err == nil {
		t.Fatal("backend error should propagate")
	}
}

// DoGetPlaybackPolicyByPlaybackID resolves the public policy and maps it to the
// model; empty id and NotFound both resolve to (nil, nil).
func TestDoGetPlaybackPolicyByPlaybackID(t *testing.T) {
	var gotID string
	c := &clientstest.FakeCommodore{
		ResolvePlaybackPolicyFn: func(_ context.Context, id string) (*commodorepb.ResolvePlaybackPolicyResponse, error) {
			gotID = id
			return &commodorepb.ResolvePlaybackPolicyResponse{Type: "jwt"}, nil
		},
	}
	pol, err := commoW2(c).DoGetPlaybackPolicyByPlaybackID(clientstest.AuthedCtx("t1"), " pb1 ")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if gotID != "pb1" {
		t.Fatalf("id not trimmed: %q", gotID)
	}
	if pol == nil || pol.Type != model.PlaybackPolicyTypeJwt {
		t.Fatalf("policy mapped wrong: %+v", pol)
	}

	// Empty id short-circuits without a backend call.
	empty := &clientstest.FakeCommodore{}
	pol, err = commoW2(empty).DoGetPlaybackPolicyByPlaybackID(clientstest.AuthedCtx("t1"), "")
	if err != nil || pol != nil {
		t.Fatalf("empty id should be (nil,nil), got (%+v,%v)", pol, err)
	}
	if empty.Calls != 0 {
		t.Fatalf("empty id must not reach backend, Calls=%d", empty.Calls)
	}

	// NotFound → (nil, nil).
	nf := commoW2(&clientstest.FakeCommodore{
		ResolvePlaybackPolicyFn: func(context.Context, string) (*commodorepb.ResolvePlaybackPolicyResponse, error) {
			return nil, status.Error(codes.NotFound, "missing")
		},
	})
	pol, err = nf.DoGetPlaybackPolicyByPlaybackID(clientstest.AuthedCtx("t1"), "pb1")
	if err != nil || pol != nil {
		t.Fatalf("NotFound should be (nil,nil), got (%+v,%v)", pol, err)
	}
}
