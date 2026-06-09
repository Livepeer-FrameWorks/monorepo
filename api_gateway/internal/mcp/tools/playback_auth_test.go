package tools

import (
	"context"
	"errors"
	"testing"

	"frameworks/api_gateway/internal/clients/clientstest"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

// ----- list_signing_keys -----

func TestHandleListSigningKeys_Success(t *testing.T) {
	commo := &clientstest.FakeCommodore{
		ListSigningKeysFn: func(_ context.Context, status string, limit int32, _ string) (*commodorepb.ListSigningKeysResponse, error) {
			// Handler defaults an unset limit to 50.
			if limit != 50 {
				t.Errorf("limit = %d, want default 50", limit)
			}
			return &commodorepb.ListSigningKeysResponse{
				SigningKeys: []*commodorepb.SigningKey{
					{Id: "k1", Kid: "kid1", Status: "active"},
					{Id: "k2", Kid: "kid2", Status: "revoked"},
				},
			}, nil
		},
	}
	sc := clientstest.Clients(clientstest.WithCommodore(commo))

	res, out, err := handleListSigningKeys(clientstest.AuthedCtx("t1"), ListSigningKeysInput{}, sc, clientstest.DiscardLogger())
	if err != nil || res.IsError {
		t.Fatalf("list should succeed: err=%v text=%s", err, extractToolText(res))
	}
	lr, ok := out.(ListSigningKeysResult)
	if !ok || len(lr.Keys) != 2 || lr.Keys[0].ID != "k1" {
		t.Fatalf("unexpected list result: %+v", out)
	}
}

func TestHandleListSigningKeys_RequiresAuth(t *testing.T) {
	// No tenant in context → Go error, and the backend is never consulted.
	commo := &clientstest.FakeCommodore{} // ListSigningKeys unstubbed → would panic if called
	sc := clientstest.Clients(clientstest.WithCommodore(commo))
	if _, _, err := handleListSigningKeys(context.Background(), ListSigningKeysInput{}, sc, clientstest.DiscardLogger()); err == nil {
		t.Fatal("missing tenant should be an auth-required Go error")
	}
	if commo.Calls != 0 {
		t.Fatalf("backend called %d times on unauthenticated request, want 0", commo.Calls)
	}
}

// ----- create_signing_key -----

func TestHandleCreateSigningKey_ConfirmationGate(t *testing.T) {
	// Wrong confirmation string must short-circuit BEFORE any backend call —
	// the destructive op never reaches Commodore.
	commo := &clientstest.FakeCommodore{}
	sc := clientstest.Clients(clientstest.WithCommodore(commo))
	res, _, err := handleCreateSigningKey(clientstest.AuthedCtx("t1"),
		CreateSigningKeyInput{Name: "k", Confirm: "create signing key"}, sc, clientstest.DiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("mismatched confirmation should be a tool error")
	}
	if commo.Calls != 0 {
		t.Fatalf("backend called %d times despite failed confirmation, want 0", commo.Calls)
	}
}

func TestHandleCreateSigningKey_EmptyNameRejected(t *testing.T) {
	commo := &clientstest.FakeCommodore{}
	sc := clientstest.Clients(clientstest.WithCommodore(commo))
	res, _, err := handleCreateSigningKey(clientstest.AuthedCtx("t1"),
		CreateSigningKeyInput{Name: "  ", Confirm: "CREATE SIGNING KEY"}, sc, clientstest.DiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("blank name should be a tool error")
	}
	if commo.Calls != 0 {
		t.Fatalf("backend called despite blank name, want 0 (got %d)", commo.Calls)
	}
}

func TestHandleCreateSigningKey_SuccessReturnsPrivateKeyOnce(t *testing.T) {
	commo := &clientstest.FakeCommodore{
		CreateSigningKeyFn: func(_ context.Context, name string) (*commodorepb.CreateSigningKeyResponse, error) {
			return &commodorepb.CreateSigningKeyResponse{
				SigningKey:    &commodorepb.SigningKey{Id: "k1", Kid: "kid1", Name: name, Status: "active"},
				PrivateKeyPem: "-----PRIVATE-----",
			}, nil
		},
	}
	sc := clientstest.Clients(clientstest.WithCommodore(commo))
	res, out, err := handleCreateSigningKey(clientstest.AuthedCtx("t1"),
		CreateSigningKeyInput{Name: "primary", Confirm: "CREATE SIGNING KEY"}, sc, clientstest.DiscardLogger())
	if err != nil || res.IsError {
		t.Fatalf("create should succeed: err=%v text=%s", err, extractToolText(res))
	}
	cr, ok := out.(CreateSigningKeyResult)
	if !ok || cr.PrivateKeyPEM != "-----PRIVATE-----" || cr.SigningKey.ID != "k1" {
		t.Fatalf("unexpected create result: %+v", out)
	}
	if cr.Warning == "" {
		t.Error("create result must warn that the private key is shown once")
	}
}

func TestHandleCreateSigningKey_BackendErrorIsToolError(t *testing.T) {
	commo := &clientstest.FakeCommodore{
		CreateSigningKeyFn: func(context.Context, string) (*commodorepb.CreateSigningKeyResponse, error) {
			return nil, errors.New("commodore down")
		},
	}
	sc := clientstest.Clients(clientstest.WithCommodore(commo))
	res, _, err := handleCreateSigningKey(clientstest.AuthedCtx("t1"),
		CreateSigningKeyInput{Name: "k", Confirm: "CREATE SIGNING KEY"}, sc, clientstest.DiscardLogger())
	if err != nil {
		t.Fatalf("backend failure should be a tool-error result, not a Go error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError on backend failure")
	}
}

// ----- revoke_signing_key -----

func TestHandleRevokeSigningKey(t *testing.T) {
	commo := &clientstest.FakeCommodore{
		RevokeSigningKeyFn: func(_ context.Context, id string) (*commodorepb.SigningKey, error) {
			return &commodorepb.SigningKey{Id: id, Status: "revoked"}, nil
		},
	}
	sc := clientstest.Clients(clientstest.WithCommodore(commo))

	// Confirmation gate.
	res, _, _ := handleRevokeSigningKey(clientstest.AuthedCtx("t1"), RevokeSigningKeyInput{ID: "k1", Confirm: "nope"}, sc, clientstest.DiscardLogger())
	if !res.IsError {
		t.Fatal("bad confirmation should be a tool error")
	}
	// Missing ID after good confirmation.
	res, _, _ = handleRevokeSigningKey(clientstest.AuthedCtx("t1"), RevokeSigningKeyInput{ID: "", Confirm: "REVOKE SIGNING KEY"}, sc, clientstest.DiscardLogger())
	if !res.IsError {
		t.Fatal("missing id should be a tool error")
	}
	if commo.Calls != 0 {
		t.Fatalf("backend should not be called before validation passes, got %d calls", commo.Calls)
	}
	// Success.
	res, out, err := handleRevokeSigningKey(clientstest.AuthedCtx("t1"), RevokeSigningKeyInput{ID: "k1", Confirm: "REVOKE SIGNING KEY"}, sc, clientstest.DiscardLogger())
	if err != nil || res.IsError {
		t.Fatalf("revoke should succeed: err=%v text=%s", err, extractToolText(res))
	}
	if sk := out.(SigningKeyResult); sk.ID != "k1" || sk.Status != "revoked" {
		t.Fatalf("unexpected revoke result: %+v", sk)
	}
}

// ----- set_playback_policy / clear_playback_policy -----

func TestHandleSetPlaybackPolicy_TargetAndTypeValidation(t *testing.T) {
	commo := &clientstest.FakeCommodore{} // never reached
	sc := clientstest.Clients(clientstest.WithCommodore(commo))
	ctx := clientstest.AuthedCtx("t1")
	good := "SET PLAYBACK POLICY"

	cases := []struct {
		name string
		in   SetPlaybackPolicyInput
	}{
		{"no target", SetPlaybackPolicyInput{Type: "public", Confirm: good}},
		{"two targets", SetPlaybackPolicyInput{StreamID: "s1", ClipID: "c1", Type: "public", Confirm: good}},
		{"unknown type", SetPlaybackPolicyInput{StreamID: "s1", Type: "bogus", Confirm: good}},
		{"webhook missing url", SetPlaybackPolicyInput{StreamID: "s1", Type: "webhook", Confirm: good}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, _, err := handleSetPlaybackPolicy(ctx, tc.in, sc, clientstest.DiscardLogger())
			if err != nil {
				t.Fatal(err)
			}
			if !res.IsError {
				t.Fatalf("%s should be a tool error", tc.name)
			}
		})
	}
	if commo.Calls != 0 {
		t.Fatalf("backend should never be called on invalid input, got %d", commo.Calls)
	}
}

func TestHandleSetPlaybackPolicy_JWTSuccess(t *testing.T) {
	var gotReq *commodorepb.SetPlaybackPolicyRequest
	commo := &clientstest.FakeCommodore{
		SetPlaybackPolicyFn: func(_ context.Context, req *commodorepb.SetPlaybackPolicyRequest) (*commodorepb.SetPlaybackPolicyResponse, error) {
			gotReq = req
			return &commodorepb.SetPlaybackPolicyResponse{StreamId: req.StreamId, RequiresAuth: true}, nil
		},
	}
	sc := clientstest.Clients(clientstest.WithCommodore(commo))
	in := SetPlaybackPolicyInput{
		StreamID:    "s1",
		Type:        "jwt",
		AllowedKids: []string{"kid1"},
		Confirm:     "SET PLAYBACK POLICY",
	}
	res, out, err := handleSetPlaybackPolicy(clientstest.AuthedCtx("t1"), in, sc, clientstest.DiscardLogger())
	if err != nil || res.IsError {
		t.Fatalf("jwt policy should succeed: err=%v text=%s", err, extractToolText(res))
	}
	// The handler must forward the type and JWT allowed-kids to Commodore intact.
	if gotReq == nil || gotReq.Type != "jwt" || gotReq.Jwt == nil || len(gotReq.Jwt.AllowedKids) != 1 {
		t.Fatalf("request not built correctly: %+v", gotReq)
	}
	if pr := out.(PlaybackPolicyResult); pr.StreamID != "s1" || !pr.RequiresAuth {
		t.Fatalf("unexpected policy result: %+v", pr)
	}
}

func TestHandleClearPlaybackPolicy_SetsPublic(t *testing.T) {
	var gotReq *commodorepb.SetPlaybackPolicyRequest
	commo := &clientstest.FakeCommodore{
		SetPlaybackPolicyFn: func(_ context.Context, req *commodorepb.SetPlaybackPolicyRequest) (*commodorepb.SetPlaybackPolicyResponse, error) {
			gotReq = req
			return &commodorepb.SetPlaybackPolicyResponse{StreamId: req.StreamId, RequiresAuth: false}, nil
		},
	}
	sc := clientstest.Clients(clientstest.WithCommodore(commo))
	res, out, err := handleClearPlaybackPolicy(clientstest.AuthedCtx("t1"),
		ClearPlaybackPolicyInput{StreamID: "s1", Confirm: "CLEAR PLAYBACK POLICY"}, sc, clientstest.DiscardLogger())
	if err != nil || res.IsError {
		t.Fatalf("clear should succeed: err=%v text=%s", err, extractToolText(res))
	}
	// Clearing is modeled as setting the policy back to "public".
	if gotReq == nil || gotReq.Type != "public" {
		t.Fatalf("clear must set type=public, got %+v", gotReq)
	}
	if pr := out.(PlaybackPolicyResult); pr.RequiresAuth {
		t.Fatalf("cleared policy should not require auth: %+v", pr)
	}
}
