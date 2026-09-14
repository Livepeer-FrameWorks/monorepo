package federation

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	goredis "github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/proto"
)

var (
	ErrPlacementReceiptUnavailable = errors.New("placement receipt storage unavailable")
	ErrPlacementReceiptConflict    = errors.New("placement attempt conflicts with its recorded intent")
	ErrPlacementReceiptMissing     = errors.New("placement attempt has no recorded intent")
	ErrPlacementReceiptExpired     = errors.New("placement attempt is expired")
)

// PlacementPullBinding authorizes a destination connection to use a physical
// pull. Fresh authorization can reuse the pull without relabeling its attempt.
type PlacementPullBinding struct {
	AttemptID        string `json:"attempt_id"`
	SourceCellID     string `json:"source_cell_id"`
	SourceClusterID  string `json:"source_cluster_id"`
	SourceNodeID     string `json:"source_node_id"`
	SourceGeneration string `json:"source_generation"`
	SourceRevision   int64  `json:"source_revision"`
	DestinationFence int64  `json:"destination_fence"`
}

type PlacementReceipt struct {
	Fresh    bool
	Request  *placementpb.PreparePlacementRequest
	Pull     *PlacementPullBinding
	Response *placementpb.Preparation
}

// PlacementReceiptStore requires shared coordination storage and has no local
// fallback. Fresh identifies a new receipt, not permission to start media; source
// ownership and physical pull state require reconciliation.
type PlacementReceiptStore struct {
	Client goredis.UniversalClient
	CellID string
	Now    func() time.Time
}

// Redis time independently fences mutations. No script renews a receipt TTL.
// UUID issuance bounds valid replays before TTL collection can remove the key.
// Engine/primary changes, eviction and a missing epoch reject earlier-issued
// attempts, including receipts lost by asynchronous persistence or replication.
// Two skew windows cover both coordinator-to-old-engine and old-to-new-engine
// clock differences before admitting attempts against a new epoch.
var placementReceiptScript = goredis.NewScript(`
local clock = redis.call('TIME')
local now = tonumber(clock[1]) * 1000 + math.floor(tonumber(clock[2]) / 1000)
local issued = tonumber(ARGV[4])
local expires = tonumber(ARGV[5])
local skew = tonumber(ARGV[8])
local lifetime = tonumber(ARGV[9])
local retention = tonumber(ARGV[10])
if now + skew < issued or expires <= now or expires > issued + lifetime then return {'expired'} end
local server = redis.call('INFO', 'server')
local replication = redis.call('INFO', 'replication')
local stats = redis.call('INFO', 'stats')
local runID = string.match(server, '\r?\nrun_id:(%x+)\r?\n')
local replID = string.match(replication, '\r?\nmaster_replid:(%x+)\r?\n')
local role = string.match(replication, '\r?\nrole:(%a+)\r?\n')
local evicted = string.match(stats, '\r?\nevicted_keys:(%d+)\r?\n')
if not runID or #runID ~= 40 or not replID or #replID ~= 40 or role ~= 'master' or not evicted then return {'unavailable'} end
local incarnation = runID .. ':' .. replID .. ':' .. evicted
if redis.call('HGET', KEYS[2], 'incarnation') ~= incarnation then
  redis.call('HSET', KEYS[2], 'incarnation', incarnation, 'not_before', now + 2 * skew + 1)
  return {'unavailable'}
end
local watermark = redis.call('HGET', KEYS[2], 'not_before')
if not watermark or not string.match(watermark, '^%d+$') then return {'unavailable'} end
local notBefore = tonumber(watermark)
if not notBefore or issued < notBefore then return {'unavailable'} end
local digest = redis.call('HGET', KEYS[1], 'digest')
if digest and digest ~= ARGV[2] then return {'conflict'} end
if digest and redis.call('HGET', KEYS[1], 'request') ~= ARGV[3] then return {'unavailable'} end
if not digest and redis.call('EXISTS', KEYS[1]) == 1 then return {'unavailable'} end
local operation = ARGV[1]
if operation == 'begin' then
  local outcome = 'existing'
  if not digest then
    redis.call('HSET', KEYS[1], 'digest', ARGV[2], 'request', ARGV[3])
    redis.call('PEXPIREAT', KEYS[1], issued + retention)
    outcome = 'created'
  end
  return {outcome, redis.call('HGET', KEYS[1], 'request') or '', redis.call('HGET', KEYS[1], 'pull') or '', redis.call('HGET', KEYS[1], 'response') or ''}
end
if not digest then return {'missing'} end
if operation == 'read' then
  local expires = tonumber(redis.call('HGET', KEYS[1], 'response_expires'))
  if not expires or expires <= now then return {'expired'} end
  return {'existing', redis.call('HGET', KEYS[1], 'request') or '', redis.call('HGET', KEYS[1], 'pull') or '', redis.call('HGET', KEYS[1], 'response') or ''}
end
if operation == 'bind' then
  local pull = redis.call('HGET', KEYS[1], 'pull')
  if pull then
    if pull == ARGV[6] then return {'ok'} end
    return {'conflict'}
  end
  if redis.call('HEXISTS', KEYS[1], 'response') == 1 then return {'conflict'} end
  redis.call('HSET', KEYS[1], 'pull', ARGV[6])
  return {'ok'}
end
if operation == 'finish' then
  if (redis.call('HGET', KEYS[1], 'pull') or '') ~= ARGV[7] then return {'conflict'} end
  local responseExpires = tonumber(ARGV[11])
  if not responseExpires or responseExpires <= now or responseExpires > expires then return {'expired'} end
  local response = redis.call('HGET', KEYS[1], 'response')
  if response and response ~= ARGV[6] then return {'conflict'} end
  redis.call('HSET', KEYS[1], 'response', ARGV[6], 'response_expires', ARGV[11])
  if ARGV[12] == 'source' then
    local currentExpiry = tonumber(redis.call('HGET', KEYS[3], 'expires')) or 0
    if currentExpiry < responseExpires then
      redis.call('HSET', KEYS[3], 'request', ARGV[3], 'expires', ARGV[11])
      redis.call('PEXPIREAT', KEYS[3], responseExpires)
    end
  end
  return {'ok'}
end
return {'invalid'}
`)

var placementReceiptEpochScript = goredis.NewScript(`
local clock = redis.call('TIME')
local now = tonumber(clock[1]) * 1000 + math.floor(tonumber(clock[2]) / 1000)
local skew = tonumber(ARGV[1])
local server = redis.call('INFO', 'server')
local replication = redis.call('INFO', 'replication')
local stats = redis.call('INFO', 'stats')
local runID = string.match(server, '\r?\nrun_id:(%x+)\r?\n')
local replID = string.match(replication, '\r?\nmaster_replid:(%x+)\r?\n')
local role = string.match(replication, '\r?\nrole:(%a+)\r?\n')
local evicted = string.match(stats, '\r?\nevicted_keys:(%d+)\r?\n')
if not runID or #runID ~= 40 or not replID or #replID ~= 40 or role ~= 'master' or not evicted then
  return {'unavailable'}
end
local incarnation = runID .. ':' .. replID .. ':' .. evicted
local recorded = redis.call('HGET', KEYS[1], 'incarnation')
local watermark = redis.call('HGET', KEYS[1], 'not_before')
if recorded ~= incarnation or not watermark or not string.match(watermark, '^%d+$') then
  local notBefore = now + 2 * skew + 1
  redis.call('HSET', KEYS[1], 'incarnation', incarnation, 'not_before', notBefore)
  return {'wait', tostring(notBefore - now)}
end
local notBefore = tonumber(watermark)
if not notBefore then return {'unavailable'} end
if notBefore <= now then return {'ready'} end
return {'wait', tostring(notBefore - now)}
`)

// AwaitReady establishes and observes the Redis incarnation fence before this
// replica advertises placement enforcement. The fence still resets on a later
// Redis primary change, so requests issued before that change remain rejected.
func (store *PlacementReceiptStore) AwaitReady(ctx context.Context) error {
	for {
		delay, err := store.epochReadyDelay(ctx)
		if err != nil {
			return err
		}
		if delay == 0 {
			return nil
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (store *PlacementReceiptStore) epochReadyDelay(ctx context.Context) (time.Duration, error) {
	if store == nil || store.Client == nil || store.CellID == "" || strings.ContainsAny(store.CellID, "{}") {
		return 0, ErrPlacementReceiptUnavailable
	}
	epochKey := fmt.Sprintf("{%s}:placement_attempt_epoch", store.CellID)
	values, err := placementReceiptEpochScript.Run(ctx, store.Client, []string{epochKey}, placement.PreparationClockSkew.Milliseconds()).StringSlice()
	if err != nil || len(values) == 0 {
		return 0, ErrPlacementReceiptUnavailable
	}
	switch values[0] {
	case "ready":
		return 0, nil
	case "wait":
		if len(values) != 2 {
			return 0, ErrPlacementReceiptUnavailable
		}
		millis, parseErr := strconv.ParseInt(values[1], 10, 64)
		if parseErr != nil || millis <= 0 || millis > 2*placement.PreparationClockSkew.Milliseconds()+1 {
			return 0, ErrPlacementReceiptUnavailable
		}
		return time.Duration(millis) * time.Millisecond, nil
	default:
		return 0, ErrPlacementReceiptUnavailable
	}
}

// Begin records immutable intent before preparation effects. A non-fresh pending
// receipt requires reconciliation, not an unconditional second start.
func (store *PlacementReceiptStore) Begin(ctx context.Context, req *placementpb.PreparePlacementRequest) (PlacementReceipt, error) {
	values, err := store.run(ctx, "begin", req, nil, nil, nil)
	if err != nil {
		return PlacementReceipt{}, err
	}
	return store.decodeReceipt(req, values)
}

func (store *PlacementReceiptStore) decodeReceipt(req *placementpb.PreparePlacementRequest, values []string) (PlacementReceipt, error) {
	if len(values) != 4 || (values[0] != "created" && values[0] != "existing") {
		return PlacementReceipt{}, ErrPlacementReceiptUnavailable
	}
	recorded := &placementpb.PreparePlacementRequest{}
	if decodeErr := proto.Unmarshal([]byte(values[1]), recorded); decodeErr != nil {
		return PlacementReceipt{}, ErrPlacementReceiptUnavailable
	}
	expected, _, err := placement.PreparationIdentity(req)
	if err != nil {
		return PlacementReceipt{}, err
	}
	actual, _, err := placement.PreparationIdentity(recorded)
	if err != nil || actual != expected {
		return PlacementReceipt{}, ErrPlacementReceiptConflict
	}
	receipt := PlacementReceipt{Fresh: values[0] == "created", Request: recorded}
	if values[2] != "" {
		pull := &PlacementPullBinding{}
		decoder := json.NewDecoder(strings.NewReader(values[2]))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(pull); err != nil || !validPlacementPullBinding(req, pull) {
			return PlacementReceipt{}, ErrPlacementReceiptUnavailable
		}
		canonical, err := json.Marshal(pull)
		if err != nil || !bytes.Equal(canonical, []byte(values[2])) {
			return PlacementReceipt{}, ErrPlacementReceiptUnavailable
		}
		receipt.Pull = pull
	}
	if values[3] != "" {
		response := &placementpb.Preparation{}
		if err := proto.Unmarshal([]byte(values[3]), response); err != nil {
			return PlacementReceipt{}, ErrPlacementReceiptUnavailable
		}
		if err := placement.ValidatePreparationResponse(req, response, store.now()); err != nil {
			return PlacementReceipt{}, ErrPlacementReceiptUnavailable
		}
		receipt.Response = response
	}
	return receipt, nil
}

// BindPull freezes the exact physical source/pull before external effects. It
// cannot replace a different binding or attach a pull to a completed receipt.
func (store *PlacementReceiptStore) BindPull(ctx context.Context, req *placementpb.PreparePlacementRequest, pull *PlacementPullBinding) error {
	if !validPlacementPullBinding(req, pull) {
		return ErrPlacementReceiptConflict
	}
	payload, err := json.Marshal(pull)
	if err != nil {
		return err
	}
	_, err = store.run(ctx, "bind", req, payload, nil, nil)
	return err
}

// Finish records only a fully bound acknowledgement. Repeating the identical
// result is allowed; a changed result cannot overwrite an earlier outcome.
func (store *PlacementReceiptStore) Finish(ctx context.Context, req *placementpb.PreparePlacementRequest, response *placementpb.Preparation, pull *PlacementPullBinding) error {
	if store == nil {
		return ErrPlacementReceiptUnavailable
	}
	if err := placement.ValidatePreparationResponse(req, response, store.now()); err != nil {
		return err
	}
	var expectedPull []byte
	if pull != nil {
		if !validPlacementPullBinding(req, pull) {
			return ErrPlacementReceiptConflict
		}
		var err error
		expectedPull, err = json.Marshal(pull)
		if err != nil {
			return err
		}
	}
	payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(response)
	if err != nil {
		return err
	}
	completion := &placementReceiptCompletion{ExpiresAt: response.ExpiresAt.AsTime()}
	if pull != nil && response.Outcome == placementpb.PreparationOutcome_PREPARATION_OUTCOME_ACCEPTED && response.TenantAuthorityVersion > 0 && response.ObjectAuthorityVersion > 0 {
		completion.SourceKey, err = store.sourceReceiptKey(PlacementSourceReceiptQuery{
			TenantID: req.Query.TenantId, InternalName: req.Query.InternalName, ClusterID: req.ClusterId, NodeID: req.NodeId,
			TenantAuthorityVersion: response.TenantAuthorityVersion, ObjectAuthorityVersion: response.ObjectAuthorityVersion, Pull: *pull,
		})
		if err != nil {
			return err
		}
	}
	_, err = store.run(ctx, "finish", req, payload, expectedPull, completion)
	return err
}

type placementReceiptCompletion struct {
	ExpiresAt time.Time
	SourceKey string
}

func (store *PlacementReceiptStore) run(ctx context.Context, operation string, req *placementpb.PreparePlacementRequest, payload, expectedPull []byte, completion *placementReceiptCompletion) ([]string, error) {
	if store == nil || store.Client == nil || store.CellID == "" || strings.ContainsAny(store.CellID, "{}") {
		return nil, ErrPlacementReceiptUnavailable
	}
	if err := placement.ValidatePreparationDeadline(req, store.now()); err != nil {
		return nil, err
	}
	digest, request, err := placement.PreparationIdentity(req)
	if err != nil {
		return nil, err
	}
	issued, err := placement.PreparationIssuedAt(req.GetAttemptId())
	if err != nil {
		return nil, err
	}
	key := fmt.Sprintf("{%s}:placement_attempt:%d:%s:%s", store.CellID, len(req.Query.TenantId), req.Query.TenantId, req.AttemptId)
	epochKey := fmt.Sprintf("{%s}:placement_attempt_epoch", store.CellID)
	indexKey, indexKind, responseExpires := key, "", int64(0)
	if completion != nil {
		responseExpires = completion.ExpiresAt.UnixMilli()
		if completion.SourceKey != "" {
			indexKey, indexKind = completion.SourceKey, "source"
		}
	}
	values, err := placementReceiptScript.Run(ctx, store.Client, []string{key, epochKey, indexKey}, operation, hex.EncodeToString(digest[:]), request,
		issued.UnixMilli(), req.ExpiresAt.AsTime().UnixMilli(), payload, expectedPull,
		placement.PreparationClockSkew.Milliseconds(), placement.PreparationLifetime.Milliseconds(),
		(placement.PreparationLifetime + 5*time.Second).Milliseconds(), responseExpires, indexKind).StringSlice()
	if err != nil || len(values) == 0 {
		return nil, ErrPlacementReceiptUnavailable
	}
	switch values[0] {
	case "created", "existing", "ok":
		return values, nil
	case "conflict":
		return nil, ErrPlacementReceiptConflict
	case "expired":
		return nil, ErrPlacementReceiptExpired
	case "missing":
		return nil, ErrPlacementReceiptMissing
	default:
		return nil, ErrPlacementReceiptUnavailable
	}
}

func (store *PlacementReceiptStore) now() time.Time {
	if store.Now != nil {
		return store.Now().UTC()
	}
	return time.Now().UTC()
}

func validPlacementPullBinding(req *placementpb.PreparePlacementRequest, pull *PlacementPullBinding) bool {
	if req == nil || req.Query == nil || req.Query.Verb != placementpb.Verb_VERB_SERVE || pull == nil ||
		pull.SourceGeneration != req.Query.SourceGeneration || !validPullGeneration(pull.SourceGeneration, pull.SourceRevision) {
		return false
	}
	if !canonicalPullAttempt(pull.AttemptID) || pull.DestinationFence <= 0 {
		return false
	}
	for _, identity := range []string{pull.SourceCellID, pull.SourceClusterID, pull.SourceNodeID} {
		if identity == "" || len(identity) > 255 || strings.TrimSpace(identity) != identity || strings.IndexFunc(identity, unicode.IsControl) >= 0 {
			return false
		}
	}
	return true
}
