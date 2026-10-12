package resolvers

import (
	"context"
	"sync"
	"testing"
	"time"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/middleware"
	signalmanclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/signalman"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	signalmanpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/signalman"
)

// gatedBroker is a signalmanBroker whose stream opens can be held, as when
// Signalman is unreachable while a stream connects or reconnects. An event
// published while no stream is open reaches nobody.
type gatedBroker struct {
	*signalmanBroker
	mu   sync.Mutex
	gate chan struct{}
}

func newGatedBroker() *gatedBroker {
	return &gatedBroker{signalmanBroker: newSignalmanBroker()}
}

// hold makes every later Open wait until release.
func (b *gatedBroker) hold() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.gate == nil {
		b.gate = make(chan struct{})
	}
}

func (b *gatedBroker) release() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.gate != nil {
		close(b.gate)
		b.gate = nil
	}
}

func (b *gatedBroker) Open(ctx context.Context, addr string, key signalmanclient.StreamKey) (signalmanclient.EventStream, error) {
	b.mu.Lock()
	gate := b.gate
	b.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return b.signalmanBroker.Open(ctx, addr, key)
}

// disconnect ends every open stream of tenantID on channel, as a Signalman
// replica restart does.
func (b *gatedBroker) disconnect(tenantID string, channel signalmanpb.Channel) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.signalmanBroker.mu.Lock()
	defer b.signalmanBroker.mu.Unlock()
	for stream := range b.streams {
		if stream.key.TenantID == tenantID && stream.key.Channel == channel {
			stream.Close()
		}
	}
}

// revocableValidator answers ValidateAPIToken from tokens and stops
// accepting a token once revoke is called, as Commodore does after it
// committed the revocation.
type revocableValidator struct {
	commodorepb.UnimplementedInternalServiceServer
	mu      sync.Mutex
	tokens  map[string]*commodorepb.ValidateAPITokenResponse
	revoked map[string]bool
}

func (v *revocableValidator) ValidateAPIToken(_ context.Context, req *commodorepb.ValidateAPITokenRequest) (*commodorepb.ValidateAPITokenResponse, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if resp, ok := v.tokens[req.GetToken()]; ok && !v.revoked[req.GetToken()] {
		return resp, nil
	}
	return &commodorepb.ValidateAPITokenResponse{Valid: false}, nil
}

func (v *revocableValidator) revoke(token string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.revoked[token] = true
}

func revocableClients(t *testing.T, tokens map[string]*commodorepb.ValidateAPITokenResponse) (*clients.ServiceClients, *revocableValidator) {
	t.Helper()
	v := &revocableValidator{tokens: tokens, revoked: map[string]bool{}}
	return internalServiceClients(t, v), v
}

// A revocation committed while the replica's watch stream is still opening
// reaches no stream. Once the stream opens the replica re-checks the
// tenant's sockets, so the revoked token's socket closes within two seconds
// instead of at the minute-long re-check.
func TestAPITokenRevokedWhileWatchStreamOpensClosesSocketOnceStreamIsLive(t *testing.T) {
	const tenantID = "tenant-1"
	sc, validator := revocableClients(t, map[string]*commodorepb.ValidateAPITokenResponse{
		"revoked-token-value": {Valid: true, UserId: "user-1", TenantId: tenantID, Role: "owner", TokenId: "token-1"},
		"other-token-value":   {Valid: true, UserId: "user-1", TenantId: tenantID, Role: "owner", TokenId: "token-2"},
	})
	broker := newGatedBroker()
	broker.hold()
	replica := newBridgeReplica(t, broker, sc)

	conn := openTokenSocket(t, replica, "revoked-token-value")
	other := openTokenSocket(t, replica, "other-token-value")

	validator.revoke("revoked-token-value")
	broker.publish(t, tenantID, signalmanpb.Channel_CHANNEL_EVENTS, apiTokenRevokedEvent(t, tenantID, "token-1"))
	broker.release()

	if code := socketCloseCode(t, conn, 2*time.Second); code != middleware.WebsocketCloseForbidden {
		t.Fatalf("socket of a token revoked while the watch stream opened closed with %d within 2s, want %d (0 = still open)", code, middleware.WebsocketCloseForbidden)
	}
	if code := socketCloseCode(t, other, 300*time.Millisecond); code != 0 {
		t.Fatalf("socket of a valid token closed with %d, want it open", code)
	}
}

// A revocation committed while the watch's upstream stream reconnects
// reaches no stream; the re-check after the stream is live again closes the
// socket within two seconds.
func TestAPITokenRevokedWhileWatchStreamReconnectsClosesSocketOnceStreamIsLive(t *testing.T) {
	const tenantID = "tenant-1"
	sc, validator := revocableClients(t, map[string]*commodorepb.ValidateAPITokenResponse{
		"revoked-token-value": {Valid: true, UserId: "user-1", TenantId: tenantID, Role: "owner", TokenId: "token-1"},
	})
	broker := newGatedBroker()
	replica := newBridgeReplica(t, broker, sc)

	conn := openTokenSocket(t, replica, "revoked-token-value")
	broker.waitStreams(t, tenantID, signalmanpb.Channel_CHANNEL_EVENTS, 1)

	broker.hold()
	broker.disconnect(tenantID, signalmanpb.Channel_CHANNEL_EVENTS)
	validator.revoke("revoked-token-value")
	broker.publish(t, tenantID, signalmanpb.Channel_CHANNEL_EVENTS, apiTokenRevokedEvent(t, tenantID, "token-1"))
	broker.release()

	if code := socketCloseCode(t, conn, 2*time.Second); code != middleware.WebsocketCloseForbidden {
		t.Fatalf("socket of a token revoked while the watch stream reconnected closed with %d within 2s, want %d (0 = still open)", code, middleware.WebsocketCloseForbidden)
	}
}
