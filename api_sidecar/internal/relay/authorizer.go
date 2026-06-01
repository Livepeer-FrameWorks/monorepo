package relay

import (
	"context"

	"frameworks/api_sidecar/internal/control"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

// RelayPullAuthorizer decides whether an inbound peer-relay pull that
// presented an opaque grant id is allowed. The serving edge holds no signing
// key — it asks Foghorn, which matches the grant it minted at resolve time
// against the artifact + exact request path. Tests inject a fake.
type RelayPullAuthorizer interface {
	AuthorizeRelayPull(ctx context.Context, grantID, artifactHash, requestPath string) (bool, error)
}

// controlAuthorizer is the production authorizer: it asks Foghorn over the
// established control stream. Errors propagate so the middleware can fail
// closed (deny) rather than guess.
type controlAuthorizer struct{}

// NewControlAuthorizer returns an authorizer backed by the Helmsman↔Foghorn
// control stream.
func NewControlAuthorizer() RelayPullAuthorizer { return &controlAuthorizer{} }

func (a *controlAuthorizer) AuthorizeRelayPull(ctx context.Context, grantID, artifactHash, requestPath string) (bool, error) {
	id, err := newRequestID()
	if err != nil {
		return false, err
	}
	resp, err := control.RequestAuthorizeRelayPull(ctx, &pb.AuthorizeRelayPullRequest{
		RequestId:    id,
		GrantId:      grantID,
		ArtifactHash: artifactHash,
		RequestPath:  requestPath,
	})
	if err != nil {
		return false, err
	}
	return resp.GetAllowed(), nil
}
