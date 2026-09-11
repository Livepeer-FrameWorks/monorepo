package placement

import (
	"crypto/sha256"
	"errors"
	"slices"
	"time"

	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

const (
	PreparationLifetime = 30 * time.Second
	// PreparationClockSkew is the maximum pairwise skew between coordinators,
	// destination Foghorns and their coordination engines.
	PreparationClockSkew = time.Second
)

// NewPreparationAttemptID embeds the issuance millisecond in a UUIDv7. Bounded
// issuance makes expired attempt IDs unusable after their receipts are collected.
func NewPreparationAttemptID(now time.Time) (string, error) {
	ms := now.UnixMilli()
	if now.IsZero() || ms < 0 || ms >= 1<<48 {
		return "", errors.New("invalid placement issuance time")
	}
	id, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	for i := 5; i >= 0; i-- {
		id[i] = byte(ms)
		ms >>= 8
	}
	return id.String(), nil
}

func PreparationIssuedAt(attempt string) (time.Time, error) {
	id, err := uuid.Parse(attempt)
	if err != nil || id.String() != attempt || id.Version() != 7 || id.Variant() != uuid.RFC4122 {
		return time.Time{}, errors.New("canonical UUIDv7 placement attempt required")
	}
	var ms int64
	for i := range 6 {
		ms = ms<<8 | int64(id[i])
	}
	return time.UnixMilli(ms).UTC(), nil
}

// PreparationIdentity includes the deadline and exact target, but normalizes
// cluster-set order. Policy revisions alone are not an idempotency identity.
func PreparationIdentity(request *placementpb.PreparePlacementRequest) ([32]byte, []byte, error) {
	if err := ValidatePreparationRequest(request); err != nil {
		return [32]byte{}, nil, err
	}
	canonical := proto.CloneOf(request)
	slices.Sort(canonical.Query.ClusterIds)
	payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(canonical)
	if err != nil {
		return [32]byte{}, nil, err
	}
	const domain = "frameworks/media-placement-attempt/v1\x00"
	digest := sha256.Sum256(append([]byte(domain), payload...))
	return digest, payload, nil
}
