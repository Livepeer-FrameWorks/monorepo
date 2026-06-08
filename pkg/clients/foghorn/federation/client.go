// Package foghornfed exposes the Foghorn cross-cluster federation gRPC stub,
// kept separate from the base pkg/clients/foghorn client so that consumers of
// only Foghorn's per-tenant control RPCs don't compile foghorn_federation.
package foghornfed

import (
	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"

	"google.golang.org/grpc"
)

// Client wraps the federation stub over a single connection.
type Client struct {
	federation foghornfederationpb.FoghornFederationClient
}

// connProvider is satisfied by *foghorn.GRPCClient via its Conn() accessor, so
// For() can build a federation client from a pooled Foghorn connection without
// importing the base client package.
type connProvider interface {
	Conn() *grpc.ClientConn
}

// New builds a federation client over an existing Foghorn connection.
func New(conn *grpc.ClientConn) *Client {
	return &Client{federation: foghornfederationpb.NewFoghornFederationClient(conn)}
}

// For builds a federation client from anything exposing a *grpc.ClientConn
// (e.g. a pooled *foghorn.GRPCClient).
func For(c connProvider) *Client {
	return New(c.Conn())
}

// Federation returns the FoghornFederation client for cross-cluster RPCs.
func (c *Client) Federation() foghornfederationpb.FoghornFederationClient {
	return c.federation
}
