package tools

import (
	"context"
	"errors"
	"testing"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/clients/clientstest"
	"frameworks/api_gateway/internal/mcp/preflight"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
)

// clipSetup wires a ServiceClients shared by the handler (Commodore) and the
// preflight Checker (Purser), plus an authed-tenant context.
func clipSetup(commo *clientstest.FakeCommodore, purser *clientstest.FakePurser) (*clients.ServiceClients, *preflight.Checker, context.Context) {
	sc := clientstest.Clients(clientstest.WithCommodore(commo), clientstest.WithPurser(purser))
	checker := preflight.NewChecker(sc, clientstest.DiscardLogger())
	return sc, checker, clientstest.AuthedCtx("t1")
}

func TestHandleCreateClip_Success(t *testing.T) {
	var gotReq *sharedpb.CreateClipRequest
	commo := &clientstest.FakeCommodore{
		CreateClipFn: func(_ context.Context, req *sharedpb.CreateClipRequest) (*sharedpb.CreateClipResponse, error) {
			gotReq = req
			return &sharedpb.CreateClipResponse{ClipHash: "clip-abc", Status: "processing"}, nil
		},
	}
	sc, checker, ctx := clipSetup(commo, clientstest.SolventPurser())

	res, out, err := handleCreateClip(ctx, CreateClipInput{StreamID: "s1", Title: "Highlight"}, sc, checker, clientstest.DiscardLogger())
	if err != nil || res.IsError {
		t.Fatalf("create clip should succeed: err=%v text=%s", err, extractToolText(res))
	}
	// Tenant from context must be bound into the request.
	if gotReq.TenantId != "t1" || gotReq.StreamId == nil || *gotReq.StreamId != "s1" {
		t.Fatalf("request not built with tenant/stream: %+v", gotReq)
	}
	if cr := out.(CreateClipResult); cr.ClipHash != "clip-abc" || cr.Status != "processing" {
		t.Fatalf("unexpected create result: %+v", cr)
	}
}

func TestHandleCreateClip_Validation(t *testing.T) {
	commo := &clientstest.FakeCommodore{} // CreateClip unstubbed → must not be reached
	sc, checker, ctx := clipSetup(commo, clientstest.SolventPurser())

	// Missing title.
	res, _, err := handleCreateClip(ctx, CreateClipInput{StreamID: "s1", Title: ""}, sc, checker, clientstest.DiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("missing title should be a tool error")
	}
	if commo.Calls != 0 {
		t.Fatalf("backend called despite invalid input: %d", commo.Calls)
	}
}

func TestHandleCreateClip_BlockedByBalance(t *testing.T) {
	commo := &clientstest.FakeCommodore{} // never reached — balance blocks first
	purser := clientstest.SolventPurser()
	purser.GetPrepaidBalanceFn = func(context.Context, string, string) (*purserpb.PrepaidBalance, error) {
		return &purserpb.PrepaidBalance{BalanceCents: 0}, nil
	}
	purser.GetPaymentRequirementsFn = func(context.Context, string, string) (*purserpb.PaymentRequirements, error) {
		return nil, errors.New("x402 unavailable")
	}
	sc := clientstest.Clients(clientstest.WithCommodore(commo), clientstest.WithPurser(purser))
	checker := preflight.NewChecker(sc, clientstest.DiscardLogger())

	res, _, err := handleCreateClip(clientstest.AuthedCtx("t1"), CreateClipInput{StreamID: "s1", Title: "x"}, sc, checker, clientstest.DiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("zero balance should block clip creation")
	}
	if commo.Calls != 0 {
		t.Fatalf("clip created despite insufficient balance: %d calls", commo.Calls)
	}
}

func TestHandleDeleteClip(t *testing.T) {
	commo := &clientstest.FakeCommodore{
		DeleteClipFn: func(_ context.Context, hash string) error {
			if hash != "clip-abc" {
				t.Errorf("hash = %q, want clip-abc", hash)
			}
			return nil
		},
	}
	sc := clientstest.Clients(clientstest.WithCommodore(commo))

	// Missing hash → tool error, backend untouched.
	res, _, err := handleDeleteClip(clientstest.AuthedCtx("t1"), DeleteClipInput{ClipHash: ""}, sc, clientstest.DiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("missing clip_hash should be a tool error")
	}
	if commo.Calls != 0 {
		t.Fatalf("backend called on empty hash: %d", commo.Calls)
	}

	// Success.
	res, out, err := handleDeleteClip(clientstest.AuthedCtx("t1"), DeleteClipInput{ClipHash: "clip-abc"}, sc, clientstest.DiscardLogger())
	if err != nil || res.IsError {
		t.Fatalf("delete should succeed: err=%v text=%s", err, extractToolText(res))
	}
	if dr := out.(DeleteClipResult); !dr.Deleted || dr.ClipHash != "clip-abc" {
		t.Fatalf("unexpected delete result: %+v", dr)
	}

	// Backend error → tool error.
	failing := clientstest.Clients(clientstest.WithCommodore(&clientstest.FakeCommodore{
		DeleteClipFn: func(context.Context, string) error { return errors.New("not found") },
	}))
	res, _, err = handleDeleteClip(clientstest.AuthedCtx("t1"), DeleteClipInput{ClipHash: "ghost"}, failing, clientstest.DiscardLogger())
	if err != nil {
		t.Fatalf("backend failure should be a tool-error result: %v", err)
	}
	if !res.IsError {
		t.Fatal("delete failure should surface as a tool error")
	}
}

func TestHandleCreateClip_RequiresAuth(t *testing.T) {
	commo := &clientstest.FakeCommodore{}
	sc := clientstest.Clients(clientstest.WithCommodore(commo), clientstest.WithPurser(clientstest.SolventPurser()))
	checker := preflight.NewChecker(sc, clientstest.DiscardLogger())
	if _, _, err := handleCreateClip(context.Background(), CreateClipInput{StreamID: "s1", Title: "x"}, sc, checker, clientstest.DiscardLogger()); err == nil {
		t.Fatal("missing tenant should be an auth-required Go error")
	}
}
