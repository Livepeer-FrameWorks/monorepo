package tools

import (
	"context"
	"errors"
	"testing"

	"frameworks/api_gateway/internal/clients/clientstest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
)

func keyToolCtx() context.Context {
	return context.WithValue(context.Background(), ctxkeys.KeyTenantID, "t1")
}

// Destructive key tools require an exact confirmation phrase before touching
// Commodore. The wrong phrase must short-circuit to a tool error and never
// reach the backend (an unstubbed fake would panic if it did).
func TestHandleRefreshStreamKey_ConfirmationGate(t *testing.T) {
	sc := clientstest.Clients(clientstest.WithCommodore(&clientstest.FakeCommodore{}))
	res, _, err := handleRefreshStreamKey(keyToolCtx(), RefreshStreamKeyInput{StreamID: "s1", Confirm: "nope"}, sc, clientstest.DiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("wrong confirmation should be a tool error")
	}
}

func TestHandleRefreshStreamKey_Success(t *testing.T) {
	commo := &clientstest.FakeCommodore{
		RefreshKeyFn: func(context.Context, string) (*commodorepb.RefreshStreamKeyResponse, error) {
			return &commodorepb.RefreshStreamKeyResponse{StreamId: "s1", StreamKey: "sk_rotated"}, nil
		},
	}
	sc := clientstest.Clients(clientstest.WithCommodore(commo))
	res, out, err := handleRefreshStreamKey(keyToolCtx(), RefreshStreamKeyInput{StreamID: "s1", Confirm: "ROTATE STREAM KEY"}, sc, clientstest.DiscardLogger())
	if err != nil || res.IsError {
		t.Fatalf("success expected: err=%v text=%s", err, extractToolText(res))
	}
	if rk := out.(RefreshStreamKeyResult); rk.NewStreamKey != "sk_rotated" {
		t.Fatalf("expected rotated key, got %+v", rk)
	}
}

func TestHandleRefreshStreamKey_RequiresAuth(t *testing.T) {
	sc := clientstest.Clients(clientstest.WithCommodore(&clientstest.FakeCommodore{}))
	if _, _, err := handleRefreshStreamKey(context.Background(), RefreshStreamKeyInput{StreamID: "s1", Confirm: "ROTATE STREAM KEY"}, sc, clientstest.DiscardLogger()); err == nil {
		t.Fatal("expected auth error without tenant")
	}
}

func TestHandleCreateStreamKey_ConfirmAndValidation(t *testing.T) {
	commo := &clientstest.FakeCommodore{
		CreateStreamKeyFn: func(_ context.Context, streamID, name string) (*commodorepb.StreamKeyResponse, error) {
			return &commodorepb.StreamKeyResponse{}, nil
		},
	}
	sc := clientstest.Clients(clientstest.WithCommodore(commo))

	// Wrong confirmation → tool error, no backend call.
	if res, _, _ := handleCreateStreamKey(keyToolCtx(), CreateStreamKeyInput{StreamID: "s1", Name: "k", Confirm: "wrong"}, sc, clientstest.DiscardLogger()); !res.IsError {
		t.Fatal("wrong confirmation should block")
	}
	// Correct confirmation but missing name → validation error.
	if res, _, _ := handleCreateStreamKey(keyToolCtx(), CreateStreamKeyInput{StreamID: "s1", Name: "", Confirm: "CREATE STREAM KEY"}, sc, clientstest.DiscardLogger()); !res.IsError {
		t.Fatal("missing name should block")
	}
	// Happy path.
	res, _, err := handleCreateStreamKey(keyToolCtx(), CreateStreamKeyInput{StreamID: "s1", Name: "k", Confirm: "CREATE STREAM KEY"}, sc, clientstest.DiscardLogger())
	if err != nil || res.IsError {
		t.Fatalf("create key should succeed: err=%v text=%s", err, extractToolText(res))
	}
}

func TestHandleListStreamKeys_SuccessAndError(t *testing.T) {
	commo := &clientstest.FakeCommodore{
		ListStreamKeysFn: func(_ context.Context, _ string, _ *commonpb.CursorPaginationRequest) (*commodorepb.ListStreamKeysResponse, error) {
			return &commodorepb.ListStreamKeysResponse{StreamKeys: []*commodorepb.StreamKey{{}, {}}}, nil
		},
	}
	sc := clientstest.Clients(clientstest.WithCommodore(commo))
	res, out, err := handleListStreamKeys(keyToolCtx(), ListStreamKeysInput{StreamID: "s1"}, sc, clientstest.DiscardLogger())
	if err != nil || res.IsError {
		t.Fatalf("list should succeed: %v", err)
	}
	if lk := out.(ListStreamKeysResult); len(lk.Keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(lk.Keys))
	}

	// Backend error → tool error result.
	errCommo := &clientstest.FakeCommodore{
		ListStreamKeysFn: func(context.Context, string, *commonpb.CursorPaginationRequest) (*commodorepb.ListStreamKeysResponse, error) {
			return nil, errors.New("commodore down")
		},
	}
	res, _, _ = handleListStreamKeys(keyToolCtx(), ListStreamKeysInput{StreamID: "s1"}, clientstest.Clients(clientstest.WithCommodore(errCommo)), clientstest.DiscardLogger())
	if !res.IsError {
		t.Fatal("backend error should be a tool error")
	}

	// Missing stream_id → validation error.
	res, _, _ = handleListStreamKeys(keyToolCtx(), ListStreamKeysInput{StreamID: ""}, sc, clientstest.DiscardLogger())
	if !res.IsError {
		t.Fatal("missing stream_id should block")
	}
}
