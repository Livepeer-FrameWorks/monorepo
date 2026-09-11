package federation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	goredis "github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/proto"
)

// PlacementSourceReceiptQuery identifies completed policy evidence for one
// physical pull. Authority versions come from current verified local snapshots,
// not from a media URL or a caller's claimed policy revision.
type PlacementSourceReceiptQuery struct {
	TenantID               string
	InternalName           string
	ClusterID              string
	NodeID                 string
	TenantAuthorityVersion int64
	ObjectAuthorityVersion int64
	Pull                   PlacementPullBinding
}

// CompletedPull reads evidence without creating intent, renewing its lifetime or
// contacting another cell. The caller must independently check current authority,
// destination registration and physical pull state; a receipt is not permission.
func (store *PlacementReceiptStore) CompletedPull(ctx context.Context, query PlacementSourceReceiptQuery) (PlacementReceipt, error) {
	key, err := store.sourceReceiptKey(query)
	if err != nil {
		return PlacementReceipt{}, err
	}
	payload, err := store.Client.HGet(ctx, key, "request").Bytes()
	if errors.Is(err, goredis.Nil) {
		return PlacementReceipt{}, ErrPlacementReceiptMissing
	}
	if err != nil {
		return PlacementReceipt{}, ErrPlacementReceiptUnavailable
	}
	request := &placementpb.PreparePlacementRequest{}
	if len(payload) > 1<<20 || proto.Unmarshal(payload, request) != nil ||
		request.GetQuery().GetTenantId() != query.TenantID || request.GetQuery().GetInternalName() != query.InternalName ||
		request.GetClusterId() != query.ClusterID || request.GetNodeId() != query.NodeID || !validPlacementPullBinding(request, &query.Pull) {
		return PlacementReceipt{}, ErrPlacementReceiptConflict
	}
	values, err := store.run(ctx, "read", request, nil, nil, nil)
	if err != nil {
		return PlacementReceipt{}, err
	}
	receipt, err := store.decodeReceipt(request, values)
	if err != nil {
		return PlacementReceipt{}, err
	}
	response := receipt.Response
	if receipt.Pull == nil || *receipt.Pull != query.Pull || response.GetOutcome() != placementpb.PreparationOutcome_PREPARATION_OUTCOME_ACCEPTED ||
		response.GetTenantAuthorityVersion() != query.TenantAuthorityVersion || response.GetObjectAuthorityVersion() != query.ObjectAuthorityVersion {
		return PlacementReceipt{}, ErrPlacementReceiptConflict
	}
	if err := ctx.Err(); err != nil {
		return PlacementReceipt{}, err
	}
	return receipt, nil
}

func (store *PlacementReceiptStore) sourceReceiptKey(query PlacementSourceReceiptQuery) (string, error) {
	if store == nil || store.Client == nil || store.CellID == "" || strings.ContainsAny(store.CellID, "{}") {
		return "", ErrPlacementReceiptUnavailable
	}
	for _, id := range []string{query.TenantID, query.InternalName, query.ClusterID, query.NodeID} {
		if id == "" || len(id) > 255 || strings.TrimSpace(id) != id || strings.IndexFunc(id, unicode.IsControl) >= 0 {
			return "", ErrPlacementReceiptConflict
		}
	}
	if query.TenantAuthorityVersion <= 0 || query.ObjectAuthorityVersion <= 0 || !validPlacementPullBinding(&placementpb.PreparePlacementRequest{
		Query: &placementpb.CandidateQuery{Verb: placementpb.Verb_VERB_SERVE, SourceGeneration: query.Pull.SourceGeneration},
	}, &query.Pull) {
		return "", ErrPlacementReceiptConflict
	}
	payload, err := json.Marshal(query)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return fmt.Sprintf("{%s}:placement_source_receipt:%s", store.CellID, hex.EncodeToString(digest[:])), nil
}
