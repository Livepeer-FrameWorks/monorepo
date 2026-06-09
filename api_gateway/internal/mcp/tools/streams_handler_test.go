package tools

import (
	"context"
	"errors"
	"testing"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/clients/clientstest"
	"frameworks/api_gateway/internal/mcp/preflight"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
)

// streamToolSetup wires a ServiceClients shared by both the handler (Commodore)
// and the preflight Checker (Purser), plus an authed-tenant context.
func streamToolSetup(commo *clientstest.FakeCommodore, purser *clientstest.FakePurser) (*clients.ServiceClients, *preflight.Checker, context.Context) {
	sc := clientstest.Clients(clientstest.WithCommodore(commo), clientstest.WithPurser(purser))
	checker := preflight.NewChecker(sc, clientstest.DiscardLogger())
	ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "t1")
	return sc, checker, ctx
}

func TestHandleCreateStream_Success(t *testing.T) {
	commo := &clientstest.FakeCommodore{
		CreateStreamFn: func(_ context.Context, req *commodorepb.CreateStreamRequest) (*commodorepb.CreateStreamResponse, error) {
			return &commodorepb.CreateStreamResponse{
				Id: "sid", Title: req.Title, StreamKey: "sk_live", PlaybackId: "pb", IngestMode: "push",
			}, nil
		},
	}
	sc, checker, ctx := streamToolSetup(commo, clientstest.SolventPurser())

	res, out, err := handleCreateStream(ctx, CreateStreamInput{Name: "My Stream"}, sc, checker, clientstest.DiscardLogger())
	if err != nil {
		t.Fatalf("unexpected go error: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success, got error result: %s", extractToolText(res))
	}
	cr, ok := out.(CreateStreamResult)
	if !ok {
		t.Fatalf("result type = %T, want CreateStreamResult", out)
	}
	if cr.StreamID != "sid" || cr.StreamKey != "sk_live" {
		t.Fatalf("unexpected result: %+v", cr)
	}
}

func TestHandleCreateStream_PullModeRedactsKey(t *testing.T) {
	commo := &clientstest.FakeCommodore{
		CreateStreamFn: func(_ context.Context, req *commodorepb.CreateStreamRequest) (*commodorepb.CreateStreamResponse, error) {
			return &commodorepb.CreateStreamResponse{Id: "sid", Title: "P", StreamKey: "sk_should_be_dropped", IngestMode: "pull"}, nil
		},
	}
	sc, checker, ctx := streamToolSetup(commo, clientstest.SolventPurser())

	_, out, err := handleCreateStream(ctx, CreateStreamInput{Name: "P", IngestMode: "pull"}, sc, checker, clientstest.DiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if cr := out.(CreateStreamResult); cr.StreamKey != "" {
		t.Fatalf("pull stream key should be redacted, got %q", cr.StreamKey)
	}
}

func TestHandleCreateStream_RequiresAuth(t *testing.T) {
	sc, checker, _ := streamToolSetup(&clientstest.FakeCommodore{}, clientstest.SolventPurser())
	// No tenant in context.
	_, _, err := handleCreateStream(context.Background(), CreateStreamInput{Name: "x"}, sc, checker, clientstest.DiscardLogger())
	if err == nil {
		t.Fatal("expected auth-required Go error when tenant is absent")
	}
}

func TestHandleCreateStream_ValidatesName(t *testing.T) {
	sc, checker, ctx := streamToolSetup(&clientstest.FakeCommodore{}, clientstest.SolventPurser())
	res, _, err := handleCreateStream(ctx, CreateStreamInput{Name: ""}, sc, checker, clientstest.DiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("empty name should be a tool error")
	}
}

func TestHandleCreateStream_BlockedByInsufficientBalance(t *testing.T) {
	// Solvent billing details but zero balance → preflight blocker surfaces as a
	// tool error with resolution, and the stream is never created.
	commo := &clientstest.FakeCommodore{} // CreateStream unstubbed → panic if called
	purser := clientstest.SolventPurser()
	purser.GetPrepaidBalanceFn = func(context.Context, string, string) (*purserpb.PrepaidBalance, error) {
		return &purserpb.PrepaidBalance{BalanceCents: 0}, nil
	}
	purser.GetPaymentRequirementsFn = func(context.Context, string, string) (*purserpb.PaymentRequirements, error) {
		return nil, errors.New("x402 unavailable")
	}
	sc, checker, ctx := streamToolSetup(commo, purser)

	res, _, err := handleCreateStream(ctx, CreateStreamInput{Name: "x"}, sc, checker, clientstest.DiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("insufficient balance should block stream creation")
	}
}

func TestHandleCreateStream_CommodoreErrorIsToolError(t *testing.T) {
	commo := &clientstest.FakeCommodore{
		CreateStreamFn: func(context.Context, *commodorepb.CreateStreamRequest) (*commodorepb.CreateStreamResponse, error) {
			return nil, errors.New("commodore unavailable")
		},
	}
	sc, checker, ctx := streamToolSetup(commo, clientstest.SolventPurser())
	res, _, err := handleCreateStream(ctx, CreateStreamInput{Name: "x"}, sc, checker, clientstest.DiscardLogger())
	if err != nil {
		t.Fatalf("client failure should be a tool-error result, not a Go error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError result on commodore failure")
	}
}

func TestHandleDeleteStream_SuccessAndValidation(t *testing.T) {
	commo := &clientstest.FakeCommodore{
		DeleteStreamFn: func(_ context.Context, id string) (*commodorepb.DeleteStreamResponse, error) {
			return &commodorepb.DeleteStreamResponse{StreamId: id, Message: "deleted"}, nil
		},
	}
	sc, checker, ctx := streamToolSetup(commo, clientstest.SolventPurser())

	res, out, err := handleDeleteStream(ctx, DeleteStreamInput{StreamID: "stream-123"}, sc, checker, clientstest.DiscardLogger())
	if err != nil || res.IsError {
		t.Fatalf("delete should succeed: err=%v text=%s", err, extractToolText(res))
	}
	if dr := out.(DeleteStreamResult); !dr.Deleted || dr.StreamID != "stream-123" {
		t.Fatalf("unexpected delete result: %+v", dr)
	}

	// Missing stream_id → tool error.
	res, _, err = handleDeleteStream(ctx, DeleteStreamInput{StreamID: ""}, sc, checker, clientstest.DiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("missing stream_id should be a tool error")
	}
}
