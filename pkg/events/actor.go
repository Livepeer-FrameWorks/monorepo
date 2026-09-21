package events

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
)

// Actor identifies who caused an event, in the terms Bridge's API usage
// batches record, so an audit row joins the usage rows of the same token.
type Actor struct {
	AuthType string
	UserID   string
	// TokenHash is HashIdentifier(usage hash secret, API token ID); zero when
	// the call was not made with an API token.
	TokenHash uint64
}

// HashIdentifier is the keyed identifier hash Bridge applies to user and API
// token IDs in API usage batches: the first 8 bytes of HMAC-SHA256(secret,
// value), big-endian. An empty value hashes to zero.
func HashIdentifier(secret []byte, value string) uint64 {
	if value == "" {
		return 0
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(value))
	return binary.BigEndian.Uint64(mac.Sum(nil)[:8])
}

// TokenHasher hashes API token IDs with Bridge's usage hash secret.
type TokenHasher struct {
	secret []byte
}

// NewTokenHasher returns a hasher for secret, which must be the value Bridge
// runs with (USAGE_HASH_SECRET). An empty secret is refused: Bridge replaces
// it with a per-process random key, so no other service could match its
// hashes.
func NewTokenHasher(secret string) (*TokenHasher, error) {
	if secret == "" {
		return nil, errors.New("events: token hash secret is empty")
	}
	return &TokenHasher{secret: []byte(secret)}, nil
}

// Hash returns the keyed hash of an API token ID.
func (h *TokenHasher) Hash(tokenID string) uint64 {
	return HashIdentifier(h.secret, tokenID)
}

// ActorFromContext reads the principal the gRPC auth interceptor stored in ctx.
// The token hash is set only for API-token calls, from the validated token
// record ID, which is the value Bridge hashes when that ID is known.
func ActorFromContext(ctx context.Context, hasher *TokenHasher) Actor {
	if hasher == nil {
		panic("events: ActorFromContext needs a TokenHasher")
	}
	actor := Actor{
		AuthType: ctxkeys.GetAuthType(ctx),
		UserID:   ctxkeys.GetUserID(ctx),
	}
	if actor.AuthType == "api_token" {
		actor.TokenHash = hasher.Hash(ctxkeys.GetAPITokenID(ctx))
	}
	return actor
}

// RequestActor carries actor on a service-to-service request, so a service
// called with the caller's service credentials attributes its events to the
// principal the caller acted for. A zero actor is nil.
func RequestActor(actor Actor) *commonpb.RequestActor {
	if actor == (Actor{}) {
		return nil
	}
	return &commonpb.RequestActor{AuthType: actor.AuthType, UserId: actor.UserID, TokenHash: actor.TokenHash}
}

// ActorFromRequest returns the actor a calling service named on its request,
// and false when it named none.
func ActorFromRequest(req *commonpb.RequestActor) (Actor, bool) {
	if req == nil || req.GetAuthType() == "" {
		return Actor{}, false
	}
	return Actor{AuthType: req.GetAuthType(), UserID: req.GetUserId(), TokenHash: req.GetTokenHash()}, true
}
