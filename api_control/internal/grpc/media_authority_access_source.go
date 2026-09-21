package grpc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"

	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
)

type playbackAccessSourceKey struct{}

type playbackAccessSource struct {
	revision     string
	requiresAuth bool
	policy       string
}

// The encrypted secret participates in the source revision without exposing its
// value. This comparison is possible even when decrypting or sealing is down.
func withPlaybackAccessSource(ctx context.Context, requiresAuth bool, policy, encryptedSecret string) context.Context {
	var document any
	decoder := json.NewDecoder(strings.NewReader(policy))
	decoder.UseNumber()
	if json.Valid([]byte(policy)) && decoder.Decode(&document) == nil {
		if canonical, err := json.Marshal(document); err == nil {
			policy = string(canonical)
		}
	}
	encoded := strconv.FormatBool(requiresAuth) + ":" + strconv.Quote(policy) + ":" + strconv.Quote(encryptedSecret)
	digest := sha256.Sum256([]byte(encoded))
	return context.WithValue(ctx, playbackAccessSourceKey{}, playbackAccessSource{revision: hex.EncodeToString(digest[:]), requiresAuth: requiresAuth, policy: policy})
}

// Dependency failure does not hide a change to the object's own constraints.
// Only the keys already known to the previous authority participate: absence of
// a fresh key snapshot is not evidence that any of those keys was revoked.
func (source playbackAccessSource) localPolicy(previous *mediapb.PlaybackPolicy) *mediapb.PlaybackPolicy {
	if !source.requiresAuth {
		return &mediapb.PlaybackPolicy{Kind: mediapb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_PUBLIC}
	}
	var doc policyDoc
	if json.Unmarshal([]byte(source.policy), &doc) != nil {
		return nil
	}
	if doc.Type == "webhook" {
		return &mediapb.PlaybackPolicy{Kind: mediapb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_WEBHOOK}
	}
	if doc.Type != "jwt" || doc.JWT == nil {
		return nil
	}
	return &mediapb.PlaybackPolicy{Kind: mediapb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_JWT, Jwt: &mediapb.PlaybackJwtPolicy{
		ActiveKeys: previous.GetJwt().GetActiveKeys(), AllowedKeyIds: sortedUnique(doc.JWT.AllowedKids),
		RequiredAudiences: sortedUnique(doc.JWT.RequiredAudience), RequiredClaimsJson: cloneStringMap(doc.JWT.RequiredClaimsJSON),
	}}
}

func bindPlaybackAccessSource(ctx context.Context, digest []byte, revisions []*mediapb.AuthoritySourceRevision) ([]byte, []*mediapb.AuthoritySourceRevision) {
	source, ok := ctx.Value(playbackAccessSourceKey{}).(playbackAccessSource)
	if !ok {
		return digest, revisions
	}
	// A source revision must be committed even if its compiled policy is equal,
	// otherwise a subsequent failed compile cannot identify a stored change.
	if len(digest) != 0 {
		hash := sha256.New()
		hash.Write(digest)
		hash.Write([]byte(source.revision))
		digest = hash.Sum(nil)
	}
	revisions = append(append([]*mediapb.AuthoritySourceRevision(nil), revisions...), &mediapb.AuthoritySourceRevision{Service: "commodore-playback-access", Revision: source.revision})
	return digest, revisions
}
