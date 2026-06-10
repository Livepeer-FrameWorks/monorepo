package resolvers

import (
	"context"
	"errors"
	"testing"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients/clientstest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// commoB3 wires a FakeCommodore into a Resolver — Batch-3 auth/admin seam.
func commoB3(c *clientstest.FakeCommodore) *Resolver {
	return &Resolver{Clients: clientstest.Clients(clientstest.WithCommodore(c)), Logger: clientstest.DiscardLogger()}
}

func TestDoLogin(t *testing.T) {
	var gotReq *commodorepb.LoginRequest
	commo := &clientstest.FakeCommodore{
		LoginFn: func(_ context.Context, req *commodorepb.LoginRequest) (*commodorepb.AuthResponse, error) {
			gotReq = req
			return &commodorepb.AuthResponse{Token: "jwt-abc"}, nil
		},
	}
	in := &commodorepb.LoginRequest{Email: "u@x.io", Password: "pw"}
	resp, err := commoB3(commo).DoLogin(context.Background(), in)
	if err != nil {
		t.Fatalf("DoLogin err: %v", err)
	}
	// Request is passed through unchanged; token surfaced.
	if gotReq != in || resp.Token != "jwt-abc" {
		t.Fatalf("DoLogin = (%+v), req passthrough=%v", resp, gotReq == in)
	}

	// Backend error → sanitized "authentication failed" (not leaked).
	failing := commoB3(&clientstest.FakeCommodore{
		LoginFn: func(context.Context, *commodorepb.LoginRequest) (*commodorepb.AuthResponse, error) {
			return nil, errors.New("bad creds detail")
		},
	})
	if _, derr := failing.DoLogin(context.Background(), in); derr == nil || derr.Error() != "authentication failed" {
		t.Fatalf("DoLogin error path = %v", derr)
	}
}

func TestDoRegister(t *testing.T) {
	var gotReq *commodorepb.RegisterRequest
	commo := &clientstest.FakeCommodore{
		RegisterFn: func(_ context.Context, req *commodorepb.RegisterRequest) (*commodorepb.RegisterResponse, error) {
			gotReq = req
			return &commodorepb.RegisterResponse{Success: true, Message: "ok"}, nil
		},
	}
	resp, err := commoB3(commo).DoRegister(context.Background(), "u@x.io", "pw", "Ada", "Lovelace")
	if err != nil {
		t.Fatalf("DoRegister err: %v", err)
	}
	// Inputs map into the proto request fields.
	if gotReq.Email != "u@x.io" || gotReq.Password != "pw" || gotReq.FirstName != "Ada" || gotReq.LastName != "Lovelace" {
		t.Fatalf("DoRegister req = %+v", gotReq)
	}
	if !resp.Success || resp.Message != "ok" {
		t.Fatalf("DoRegister resp = %+v", resp)
	}

	failing := commoB3(&clientstest.FakeCommodore{
		RegisterFn: func(context.Context, *commodorepb.RegisterRequest) (*commodorepb.RegisterResponse, error) {
			return nil, errors.New("dup email")
		},
	})
	if _, derr := failing.DoRegister(context.Background(), "u@x.io", "pw", "", ""); derr == nil || derr.Error() != "registration failed" {
		t.Fatalf("DoRegister error path = %v", derr)
	}
}

func TestDoGetMe(t *testing.T) {
	email := "u@x.io"
	commo := &clientstest.FakeCommodore{
		GetMeFn: func(context.Context) (*commodorepb.User, error) {
			return &commodorepb.User{Id: "u1", Email: &email}, nil
		},
	}
	// JWT token in context satisfies the auth gate.
	ctx := context.WithValue(clientstest.AuthedCtx("t1"), ctxkeys.KeyJWTToken, "tok")
	user, err := commoB3(commo).DoGetMe(ctx)
	if err != nil || user.Id != "u1" {
		t.Fatalf("DoGetMe = (%+v, %v)", user, err)
	}

	// No JWT token → auth error, backend never called.
	guard := &clientstest.FakeCommodore{
		GetMeFn: func(context.Context) (*commodorepb.User, error) { return nil, nil },
	}
	if _, derr := commoB3(guard).DoGetMe(context.Background()); derr == nil {
		t.Fatal("DoGetMe without token should error")
	}
	if guard.Calls != 0 {
		t.Fatalf("DoGetMe auth guard called backend %d times", guard.Calls)
	}

	// Backend error → sanitized message.
	failing := commoB3(&clientstest.FakeCommodore{
		GetMeFn: func(context.Context) (*commodorepb.User, error) { return nil, errors.New("boom") },
	})
	failCtx := context.WithValue(context.Background(), ctxkeys.KeyJWTToken, "tok")
	if _, derr := failing.DoGetMe(failCtx); derr == nil || derr.Error() != "failed to get user info" {
		t.Fatalf("DoGetMe error path = %v", derr)
	}
}

func TestDoWalletLogin(t *testing.T) {
	exp := time.Now().Add(time.Hour).Truncate(time.Second)
	var gotAddr, gotMsg, gotSig string
	commo := &clientstest.FakeCommodore{
		WalletLoginFn: func(_ context.Context, addr, msg, sig string, _ *commonpb.SignupAttribution) (*commodorepb.AuthResponse, error) {
			gotAddr, gotMsg, gotSig = addr, msg, sig
			return &commodorepb.AuthResponse{
				Token:     "wtok",
				ExpiresAt: timestamppb.New(exp),
				IsNewUser: true,
			}, nil
		},
	}
	in := model.WalletLoginInput{Address: "0xabc", Message: "msg", Signature: "0xsig"}
	res, err := commoB3(commo).DoWalletLogin(clientstest.AuthedCtx("t1"), in)
	if err != nil {
		t.Fatalf("DoWalletLogin err: %v", err)
	}
	// Inputs forwarded to the client verbatim.
	if gotAddr != "0xabc" || gotMsg != "msg" || gotSig != "0xsig" {
		t.Fatalf("DoWalletLogin forwarded (%q,%q,%q)", gotAddr, gotMsg, gotSig)
	}
	payload, ok := res.(*model.WalletLoginPayload)
	if !ok {
		t.Fatalf("DoWalletLogin result type = %T", res)
	}
	if payload.Token != "wtok" || !payload.IsNewAccount || !payload.ExpiresAt.Equal(exp) {
		t.Fatalf("DoWalletLogin payload = %+v", payload)
	}

	// Backend error → ValidationError union (typed, not Go error).
	failing := commoB3(&clientstest.FakeCommodore{
		WalletLoginFn: func(context.Context, string, string, string, *commonpb.SignupAttribution) (*commodorepb.AuthResponse, error) {
			return nil, errors.New("bad signature")
		},
	})
	res, err = failing.DoWalletLogin(clientstest.AuthedCtx("t1"), in)
	if err != nil {
		t.Fatalf("DoWalletLogin error path returned Go err: %v", err)
	}
	if ve, ok := res.(*model.ValidationError); !ok || ve.Code == nil || *ve.Code != "WALLET_AUTH_FAILED" {
		t.Fatalf("DoWalletLogin error union = %#v", res)
	}
}

func TestDoLinkWallet(t *testing.T) {
	created := time.Now().Truncate(time.Second)
	commo := &clientstest.FakeCommodore{
		LinkWalletFn: func(_ context.Context, addr, msg, sig string) (*commodorepb.WalletIdentity, error) {
			return &commodorepb.WalletIdentity{Id: "w1", WalletAddress: addr, CreatedAt: timestamppb.New(created)}, nil
		},
	}
	in := model.WalletLoginInput{Address: "0xdef", Message: "m", Signature: "s"}
	authCtx := context.WithValue(clientstest.AuthedCtx("t1"), ctxkeys.KeyUserID, "u1")

	res, err := commoB3(commo).DoLinkWallet(authCtx, in)
	if err != nil {
		t.Fatalf("DoLinkWallet err: %v", err)
	}
	wallet, ok := res.(*model.WalletIdentity)
	if !ok || wallet.ID != "w1" || wallet.Address != "0xdef" {
		t.Fatalf("DoLinkWallet result = %#v", res)
	}

	// Missing user ID → AuthError union, backend untouched.
	guard := &clientstest.FakeCommodore{
		LinkWalletFn: func(context.Context, string, string, string) (*commodorepb.WalletIdentity, error) { return nil, nil },
	}
	res, _ = commoB3(guard).DoLinkWallet(clientstest.AuthedCtx("t1"), in)
	if _, ok := res.(*model.AuthError); !ok {
		t.Fatalf("DoLinkWallet unauth result = %#v", res)
	}
	if guard.Calls != 0 {
		t.Fatalf("DoLinkWallet auth guard called backend %d times", guard.Calls)
	}

	// Backend error → ValidationError union.
	failing := commoB3(&clientstest.FakeCommodore{
		LinkWalletFn: func(context.Context, string, string, string) (*commodorepb.WalletIdentity, error) {
			return nil, errors.New("already linked")
		},
	})
	res, _ = failing.DoLinkWallet(authCtx, in)
	if ve, ok := res.(*model.ValidationError); !ok || ve.Code == nil || *ve.Code != "LINK_WALLET_FAILED" {
		t.Fatalf("DoLinkWallet error union = %#v", res)
	}
}

func TestDoUnlinkWallet(t *testing.T) {
	var gotID string
	commo := &clientstest.FakeCommodore{
		UnlinkWalletFn: func(_ context.Context, id string) (*commodorepb.UnlinkWalletResponse, error) {
			gotID = id
			return &commodorepb.UnlinkWalletResponse{Success: true}, nil
		},
	}
	authCtx := context.WithValue(clientstest.AuthedCtx("t1"), ctxkeys.KeyUserID, "u1")
	res, err := commoB3(commo).DoUnlinkWallet(authCtx, "w9")
	if err != nil {
		t.Fatalf("DoUnlinkWallet err: %v", err)
	}
	del, ok := res.(*model.DeleteSuccess)
	if !ok || del.DeletedID != "w9" || !del.Success || gotID != "w9" {
		t.Fatalf("DoUnlinkWallet result = %#v (gotID=%q)", res, gotID)
	}

	// Missing user ID → AuthError, backend untouched.
	guard := &clientstest.FakeCommodore{
		UnlinkWalletFn: func(context.Context, string) (*commodorepb.UnlinkWalletResponse, error) { return nil, nil },
	}
	res, _ = commoB3(guard).DoUnlinkWallet(clientstest.AuthedCtx("t1"), "w9")
	if _, ok := res.(*model.AuthError); !ok {
		t.Fatalf("DoUnlinkWallet unauth result = %#v", res)
	}
	if guard.Calls != 0 {
		t.Fatalf("DoUnlinkWallet auth guard called backend %d times", guard.Calls)
	}

	// Backend error → NotFoundError union.
	failing := commoB3(&clientstest.FakeCommodore{
		UnlinkWalletFn: func(context.Context, string) (*commodorepb.UnlinkWalletResponse, error) {
			return nil, errors.New("not found")
		},
	})
	res, _ = failing.DoUnlinkWallet(authCtx, "w9")
	if nf, ok := res.(*model.NotFoundError); !ok || nf.ResourceType != "WalletIdentity" {
		t.Fatalf("DoUnlinkWallet error union = %#v", res)
	}
}

func TestDoLinkEmail(t *testing.T) {
	var gotEmail, gotPw string
	commo := &clientstest.FakeCommodore{
		LinkEmailFn: func(_ context.Context, email, pw string) (*commodorepb.LinkEmailResponse, error) {
			gotEmail, gotPw = email, pw
			return &commodorepb.LinkEmailResponse{Success: true, Message: "sent", VerificationSent: true}, nil
		},
	}
	authCtx := context.WithValue(clientstest.AuthedCtx("t1"), ctxkeys.KeyUserID, "u1")
	in := model.LinkEmailInput{Email: "u@x.io", Password: "pw"}
	res, err := commoB3(commo).DoLinkEmail(authCtx, in)
	if err != nil {
		t.Fatalf("DoLinkEmail err: %v", err)
	}
	payload, ok := res.(*model.LinkEmailPayload)
	if !ok || !payload.Success || !payload.VerificationSent || payload.Message != "sent" {
		t.Fatalf("DoLinkEmail result = %#v", res)
	}
	if gotEmail != "u@x.io" || gotPw != "pw" {
		t.Fatalf("DoLinkEmail forwarded (%q,%q)", gotEmail, gotPw)
	}

	// Missing user ID → AuthError, backend untouched.
	guard := &clientstest.FakeCommodore{
		LinkEmailFn: func(context.Context, string, string) (*commodorepb.LinkEmailResponse, error) { return nil, nil },
	}
	res, _ = commoB3(guard).DoLinkEmail(clientstest.AuthedCtx("t1"), in)
	if _, ok := res.(*model.AuthError); !ok {
		t.Fatalf("DoLinkEmail unauth result = %#v", res)
	}
	if guard.Calls != 0 {
		t.Fatalf("DoLinkEmail auth guard called backend %d times", guard.Calls)
	}

	// Backend error → ValidationError union scoped to the email field.
	failing := commoB3(&clientstest.FakeCommodore{
		LinkEmailFn: func(context.Context, string, string) (*commodorepb.LinkEmailResponse, error) {
			return nil, errors.New("already linked")
		},
	})
	res, _ = failing.DoLinkEmail(authCtx, in)
	if ve, ok := res.(*model.ValidationError); !ok || ve.Code == nil || *ve.Code != "EMAIL_LINK_FAILED" {
		t.Fatalf("DoLinkEmail error union = %#v", res)
	}
}
