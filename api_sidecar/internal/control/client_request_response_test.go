package control

import (
	"context"
	"testing"
	"time"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// These cover the request/response round-trips Helmsman makes over the control
// stream where it blocks for Foghorn's reply, keyed by request_id. The shared
// contracts are: (1) a disconnected stream fails fast rather than hanging, and
// (2) the inbound-response router delivers to the exact waiter and silently
// ignores nil / unknown-id / duplicate responses instead of panicking or
// wedging the receive loop.

// connectFake installs a fake control stream whose Send pushes each message to
// sendCh, so a test can grab the (possibly server-generated) request_id without
// polling.
func connectFake(t *testing.T) *fakeControlStream {
	t.Helper()
	stream := &fakeControlStream{sendCh: make(chan *ipcpb.ControlMessage, 4)}
	storeConn(stream, "test-node")
	t.Cleanup(clearConn)
	return stream
}

func TestRequestRelayResolveRoundTrip(t *testing.T) {
	stream := connectFake(t)

	done := make(chan struct{})
	var resp *ipcpb.RelayResolveResponse
	var err error
	go func() {
		defer close(done)
		resp, err = RequestRelayResolve(context.Background(), &ipcpb.RelayResolveRequest{
			RequestId: "rr-1",
			AssetHash: "hash-1",
		})
	}()

	sent := waitForControlMessage(t, stream.sendCh, "relay resolve request")
	if sent.GetRelayResolveRequest() == nil {
		t.Fatalf("expected RelayResolveRequest payload, got %T", sent.GetPayload())
	}
	if sent.GetRelayResolveRequest().GetNodeId() != "test-node" {
		t.Fatalf("node id not injected: %q", sent.GetRelayResolveRequest().GetNodeId())
	}

	handleRelayResolveResponse(&ipcpb.RelayResolveResponse{
		RequestId:         "rr-1",
		AssetHash:         "hash-1",
		State:             ipcpb.AssetState_ASSET_STATE_PLAYABLE,
		MediaPresignedUrl: "https://s3.example/hash-1",
	})

	waitForTestDone(t, done, "relay resolve round trip")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetState() != ipcpb.AssetState_ASSET_STATE_PLAYABLE || resp.GetMediaPresignedUrl() != "https://s3.example/hash-1" {
		t.Fatalf("response not routed to waiter: %+v", resp)
	}
}

func TestRequestRelayResolveValidation(t *testing.T) {
	t.Run("disconnected fails fast", func(t *testing.T) {
		clearConn()
		if _, err := RequestRelayResolve(context.Background(), &ipcpb.RelayResolveRequest{RequestId: "x"}); err == nil {
			t.Fatal("expected error with no control stream")
		}
	})

	t.Run("missing request_id rejected", func(t *testing.T) {
		connectFake(t)
		if _, err := RequestRelayResolve(context.Background(), &ipcpb.RelayResolveRequest{}); err == nil {
			t.Fatal("expected error for request without request_id")
		}
	})

	t.Run("context cancellation returns", func(t *testing.T) {
		connectFake(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := RequestRelayResolve(ctx, &ipcpb.RelayResolveRequest{RequestId: "cancel-1"}); err == nil {
			t.Fatal("expected error from cancelled context")
		}
	})
}

func TestHandleRelayResolveResponseIgnoresStray(t *testing.T) {
	// None of these have a registered waiter; each must be a no-op.
	handleRelayResolveResponse(nil)
	handleRelayResolveResponse(&ipcpb.RelayResolveResponse{})                   // empty request_id
	handleRelayResolveResponse(&ipcpb.RelayResolveResponse{RequestId: "ghost"}) // unknown id
}

func TestRequestAuthorizeRelayPullRoundTrip(t *testing.T) {
	stream := connectFake(t)

	done := make(chan struct{})
	var resp *ipcpb.AuthorizeRelayPullResponse
	var err error
	go func() {
		defer close(done)
		resp, err = RequestAuthorizeRelayPull(context.Background(), &ipcpb.AuthorizeRelayPullRequest{
			RequestId: "ap-1",
		})
	}()

	sent := waitForControlMessage(t, stream.sendCh, "authorize relay pull request")
	if sent.GetAuthorizeRelayPullRequest() == nil {
		t.Fatalf("expected AuthorizeRelayPullRequest payload, got %T", sent.GetPayload())
	}

	handleAuthorizeRelayPullResponse(&ipcpb.AuthorizeRelayPullResponse{
		RequestId: "ap-1",
		Allowed:   true,
	})

	waitForTestDone(t, done, "authorize relay pull round trip")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.GetAllowed() {
		t.Fatal("expected allowed=true to be routed to waiter")
	}
}

func TestRequestAuthorizeRelayPullValidation(t *testing.T) {
	t.Run("disconnected fails closed", func(t *testing.T) {
		clearConn()
		if _, err := RequestAuthorizeRelayPull(context.Background(), &ipcpb.AuthorizeRelayPullRequest{RequestId: "x"}); err == nil {
			t.Fatal("expected error with no control stream")
		}
	})

	t.Run("missing request_id rejected", func(t *testing.T) {
		connectFake(t)
		if _, err := RequestAuthorizeRelayPull(context.Background(), &ipcpb.AuthorizeRelayPullRequest{}); err == nil {
			t.Fatal("expected error for request without request_id")
		}
	})
}

func TestHandleAuthorizeRelayPullResponseIgnoresStray(t *testing.T) {
	handleAuthorizeRelayPullResponse(nil)
	handleAuthorizeRelayPullResponse(&ipcpb.AuthorizeRelayPullResponse{})                   // empty request_id
	handleAuthorizeRelayPullResponse(&ipcpb.AuthorizeRelayPullResponse{RequestId: "ghost"}) // unknown id

	// A duplicate/late response on a full buffered channel must not block the
	// receive loop (the handler uses a non-blocking send).
	ch := make(chan *ipcpb.AuthorizeRelayPullResponse, 1)
	authorizeRelayPullMutex <- struct{}{}
	authorizeRelayPullHandlers["dup-1"] = ch
	<-authorizeRelayPullMutex
	t.Cleanup(func() {
		authorizeRelayPullMutex <- struct{}{}
		delete(authorizeRelayPullHandlers, "dup-1")
		<-authorizeRelayPullMutex
	})
	handleAuthorizeRelayPullResponse(&ipcpb.AuthorizeRelayPullResponse{RequestId: "dup-1", Allowed: true})
	handleAuthorizeRelayPullResponse(&ipcpb.AuthorizeRelayPullResponse{RequestId: "dup-1", Allowed: false}) // would block without select/default
	if got := <-ch; !got.GetAllowed() {
		t.Fatal("first response should be the one buffered")
	}
}

func TestValidateMistAdminSessionRoundTripAndCache(t *testing.T) {
	const token = "tok-roundtrip-unique"
	t.Cleanup(func() { mistAdminSessionCache.Delete(token) })

	stream := connectFake(t)

	done := make(chan struct{})
	var resp *ipcpb.EdgeMistAdminSessionResponse
	var err error
	go func() {
		defer close(done)
		resp, err = ValidateMistAdminSession(context.Background(), token)
	}()

	sent := waitForControlMessage(t, stream.sendCh, "mist admin session request")
	if sent.GetEdgeMistAdminSessionRequest().GetToken() != token {
		t.Fatalf("token not forwarded: %q", sent.GetEdgeMistAdminSessionRequest().GetToken())
	}

	// Response carries no request_id field; the router keys on the envelope's
	// request_id, which the request stamped.
	handleEdgeMistAdminSessionResponse(sent.GetRequestId(), &ipcpb.EdgeMistAdminSessionResponse{
		Valid:     true,
		TenantId:  "tenant-x",
		ExpiresAt: time.Now().Add(time.Hour).Unix(),
	})

	waitForTestDone(t, done, "mist admin session round trip")
	if err != nil || !resp.GetValid() || resp.GetTenantId() != "tenant-x" {
		t.Fatalf("unexpected result: resp=%+v err=%v", resp, err)
	}

	// Second call for the same token must hit the cache and not touch the
	// stream at all — prove it by disconnecting first.
	clearConn()
	cached, err := ValidateMistAdminSession(context.Background(), token)
	if err != nil {
		t.Fatalf("cached lookup errored: %v", err)
	}
	if !cached.GetValid() || cached.GetTenantId() != "tenant-x" {
		t.Fatalf("cache did not return the validated session: %+v", cached)
	}
}

func TestValidateMistAdminSessionDisconnected(t *testing.T) {
	clearConn()
	if _, err := ValidateMistAdminSession(context.Background(), "tok-uncached-unique"); err == nil {
		t.Fatal("expected error validating with no control stream and no cache entry")
	}
}

func TestHandleEdgeMistAdminSessionResponseIgnoresUnknown(t *testing.T) {
	// No registered waiter for this request_id; must be a no-op.
	handleEdgeMistAdminSessionResponse("ghost", &ipcpb.EdgeMistAdminSessionResponse{Valid: true})
}
