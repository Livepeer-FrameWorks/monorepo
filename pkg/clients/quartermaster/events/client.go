// Package events is the producer-side client for Quartermaster's
// service_event_outbox. It is kept separate from the base quartermaster client
// so only event producers (e.g. Deckhand) compile ipcpb.
package events

import (
	"context"
	"fmt"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

// Client enqueues service events through Quartermaster for asynchronous Decklog
// dispatch.
type Client struct {
	registry quartermasterpb.ServiceRegistryServiceClient
}

// New builds an events client over an existing Quartermaster connection. Pass
// the base client's Conn() so the dialed connection (TLS + interceptors) is
// shared.
func New(conn *grpc.ClientConn) *Client {
	return &Client{registry: quartermasterpb.NewServiceRegistryServiceClient(conn)}
}

// EnqueueServiceEvent hands a ServiceEvent to Quartermaster's
// service_event_outbox for asynchronous Decklog dispatch. Used by stateless
// producers (Deckhand) that don't own a local outbox. event.source must
// identify the originating service so the dispatcher attributes correctly. The
// event is binary-marshaled into the request's bytes field.
func (c *Client) EnqueueServiceEvent(ctx context.Context, event *ipcpb.ServiceEvent) (string, error) {
	raw, err := proto.Marshal(event)
	if err != nil {
		return "", fmt.Errorf("marshal service event: %w", err)
	}
	resp, err := c.registry.EnqueueServiceEvent(ctx, &quartermasterpb.EnqueueServiceEventRequest{
		Event: raw,
	})
	if err != nil {
		return "", err
	}
	return resp.GetOutboxId(), nil
}
