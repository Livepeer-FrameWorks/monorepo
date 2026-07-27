package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	pkgredis "github.com/Livepeer-FrameWorks/monorepo/pkg/redis"

	goredis "github.com/redis/go-redis/v9"
)

const connOwnerTTL = 60 * time.Second
const pendingDVRStopTTL = 30 * time.Minute

var ErrConnOwnerMissing = errors.New("conn owner key missing")

// deleteConnOwnerIfMatch atomically deletes the key only if its value still
// matches expectedVal, preventing a stale eviction from clobbering a fresh
// owner written by another instance during failover.
var deleteConnOwnerIfMatch = goredis.NewScript(`
if redis.call('get', KEYS[1]) == ARGV[1] then
  return redis.call('del', KEYS[1])
else
  return 0
end
`)

var getAndDelete = goredis.NewScript(`
local val = redis.call('get', KEYS[1])
if val then
  redis.call('del', KEYS[1])
end
return val
`)

var renewLeaseScript = goredis.NewScript(`
if redis.call('get', KEYS[1]) == ARGV[1] then
  return redis.call('pexpire', KEYS[1], ARGV[2])
else
  return 0
end
`)

var releaseLeaseScript = goredis.NewScript(`
if redis.call('get', KEYS[1]) == ARGV[1] then
  return redis.call('del', KEYS[1])
else
  return 0
end
`)

type StateEntity string

type StateOperation string

const (
	StateEntityNode           StateEntity = "node"
	StateEntityStream         StateEntity = "stream"
	StateEntityStreamInstance StateEntity = "stream_instance"
	StateEntityArtifact       StateEntity = "artifact"
	// StateEntityNodeMode carries a node's operational mode as its own keyed
	// record. The mode is multi-writer (operator API / orchestrator on any
	// instance) and must not ride the whole-node snapshot: an in-flight
	// heartbeat snapshot marshaled before a mode change would republish the
	// old mode at a newer changelog ID, and the node watermark would then
	// block the (older-ID) mode change.
	StateEntityNodeMode StateEntity = "node_mode"

	StateOpUpsert StateOperation = "upsert"
	StateOpDelete StateOperation = "delete"
)

type StateChange struct {
	InstanceID string          `json:"instance_id"`
	Entity     StateEntity     `json:"entity"`
	Operation  StateOperation  `json:"operation"`
	StreamName string          `json:"stream_name,omitempty"`
	NodeID     string          `json:"node_id,omitempty"`
	ArtifactID string          `json:"artifact_id,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
}

type streamInstanceRecord struct {
	InternalName string               `json:"internal_name"`
	NodeID       string               `json:"node_id"`
	State        *StreamInstanceState `json:"state"`
}

// nodeModeRecord is the write-through payload for StateEntityNodeMode.
type nodeModeRecord struct {
	NodeID string              `json:"node_id"`
	Mode   NodeOperationalMode `json:"mode"`
	SetBy  string              `json:"set_by,omitempty"`
	SetAt  time.Time           `json:"set_at"`
}

type NodeArtifactState struct {
	NodeID       string `json:"node_id"`
	ClipHash     string `json:"clip_hash"`
	FilePath     string `json:"file_path"`
	SizeBytes    uint64 `json:"size_bytes"`
	StreamName   string `json:"stream_name,omitempty"`
	ArtifactType string `json:"artifact_type,omitempty"`
	Format       string `json:"format,omitempty"`
}

// stateChangelogMaxLen bounds the state changelog stream. At prod write
// rates (a few entries/second/cluster) this retains hours of history —
// far beyond any realistic consumer downtime.
const stateChangelogMaxLen = 100000

type RedisStateStore struct {
	client    goredis.UniversalClient
	changelog *pkgredis.Changelog[StateChange]
	clusterID string
}

func NewRedisStateStore(client goredis.UniversalClient, clusterID string) *RedisStateStore {
	return &RedisStateStore{
		client:    client,
		changelog: pkgredis.NewChangelog[StateChange](client, fmt.Sprintf("{%s}:state_changelog", clusterID), stateChangelogMaxLen),
		clusterID: clusterID,
	}
}

func (r *RedisStateStore) keyNode(nodeID string) string {
	return fmt.Sprintf("{%s}:nodes:%s", r.clusterID, nodeID)
}
func (r *RedisStateStore) keyStream(streamName string) string {
	return fmt.Sprintf("{%s}:streams:%s", r.clusterID, streamName)
}
func (r *RedisStateStore) keyStreamInstance(streamName, nodeID string) string {
	return fmt.Sprintf("{%s}:stream_instances:%s:%s", r.clusterID, streamName, nodeID)
}
func (r *RedisStateStore) keyArtifact(nodeID string) string {
	return fmt.Sprintf("{%s}:artifacts:%s", r.clusterID, nodeID)
}
func (r *RedisStateStore) keyNodeMode(nodeID string) string {
	return fmt.Sprintf("{%s}:node_mode:%s", r.clusterID, nodeID)
}
func (r *RedisStateStore) keyLease(role string) string {
	return fmt.Sprintf("{%s}:lease:%s", r.clusterID, role)
}

func (r *RedisStateStore) setJSON(ctx context.Context, key string, value any) error {
	bytes, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return r.client.Set(ctx, key, bytes, 0).Err()
}

func (r *RedisStateStore) setJSONRaw(ctx context.Context, key string, payload []byte) error {
	return r.client.Set(ctx, key, payload, 0).Err()
}

func (r *RedisStateStore) TryAcquireLease(ctx context.Context, role, owner string, ttl time.Duration) (bool, error) {
	if owner == "" {
		return false, nil
	}
	key := r.keyLease(role)
	ok, err := r.client.SetNX(ctx, key, owner, ttl).Result()
	if err != nil {
		return false, err
	}
	if ok {
		return true, nil
	}
	current, err := r.client.Get(ctx, key).Result()
	if errors.Is(err, goredis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if current != owner {
		return false, nil
	}
	return r.RenewLease(ctx, role, owner, ttl)
}

func (r *RedisStateStore) RenewLease(ctx context.Context, role, owner string, ttl time.Duration) (bool, error) {
	if owner == "" {
		return false, nil
	}
	result, err := renewLeaseScript.Run(ctx, r.client, []string{r.keyLease(role)}, owner, int64(ttl/time.Millisecond)).Int64()
	return err == nil && result == 1, err
}

func (r *RedisStateStore) ReleaseLease(ctx context.Context, role, owner string) error {
	if owner == "" {
		return nil
	}
	return releaseLeaseScript.Run(ctx, r.client, []string{r.keyLease(role)}, owner).Err()
}

func (r *RedisStateStore) SetNode(nodeID string, state *NodeState) error {
	return r.setJSON(context.Background(), r.keyNode(nodeID), state)
}

func (r *RedisStateStore) GetAllNodes() (map[string]*NodeState, error) {
	return scanRedisMap(r, "{"+r.clusterID+"}:nodes:*", func(data string) (*NodeState, string, error) {
		var n NodeState
		if err := json.Unmarshal([]byte(data), &n); err != nil {
			return nil, "", err
		}
		return &n, n.NodeID, nil
	})
}

func (r *RedisStateStore) DeleteNode(nodeID string) error {
	return r.client.Del(context.Background(), r.keyNode(nodeID)).Err()
}

func (r *RedisStateStore) GetAllNodeModes() (map[string]*nodeModeRecord, error) {
	return scanRedisMap(r, "{"+r.clusterID+"}:node_mode:*", func(data string) (*nodeModeRecord, string, error) {
		var rec nodeModeRecord
		if err := json.Unmarshal([]byte(data), &rec); err != nil {
			return nil, "", err
		}
		return &rec, rec.NodeID, nil
	})
}

func (r *RedisStateStore) DeleteNodeMode(nodeID string) error {
	return r.client.Del(context.Background(), r.keyNodeMode(nodeID)).Err()
}

func (r *RedisStateStore) SetStream(name string, state *StreamState) error {
	return r.setJSON(context.Background(), r.keyStream(name), state)
}

func (r *RedisStateStore) GetAllStreams() (map[string]*StreamState, error) {
	return scanRedisMap(r, "{"+r.clusterID+"}:streams:*", func(data string) (*StreamState, string, error) {
		var s StreamState
		if err := json.Unmarshal([]byte(data), &s); err != nil {
			return nil, "", err
		}
		return &s, s.InternalName, nil
	})
}

func (r *RedisStateStore) DeleteStream(name string) error {
	return r.client.Del(context.Background(), r.keyStream(name)).Err()
}

func (r *RedisStateStore) SetStreamInstance(name, nodeID string, state *StreamInstanceState) error {
	rec := streamInstanceRecord{InternalName: name, NodeID: nodeID, State: state}
	return r.setJSON(context.Background(), r.keyStreamInstance(name, nodeID), rec)
}

func (r *RedisStateStore) GetAllStreamInstances() (map[string]map[string]*StreamInstanceState, error) {
	records, err := scanRedisMap(r, "{"+r.clusterID+"}:stream_instances:*", func(data string) (*streamInstanceRecord, string, error) {
		var rec streamInstanceRecord
		if err := json.Unmarshal([]byte(data), &rec); err != nil {
			return nil, "", err
		}
		return &rec, rec.InternalName + ":" + rec.NodeID, nil
	})
	if err != nil {
		return nil, err
	}
	result := make(map[string]map[string]*StreamInstanceState)
	for key, rec := range records {
		parts := strings.SplitN(key, ":", 2)
		if len(parts) != 2 {
			continue
		}
		if result[parts[0]] == nil {
			result[parts[0]] = make(map[string]*StreamInstanceState)
		}
		result[parts[0]][parts[1]] = rec.State
	}
	return result, nil
}

func (r *RedisStateStore) DeleteStreamInstance(name, nodeID string) error {
	return r.client.Del(context.Background(), r.keyStreamInstance(name, nodeID)).Err()
}

// NodeArtifactSnapshot is the WHOLE-NODE report ENVELOPE stored in Redis and carried on the changelog.
// Its (Fence, Seq) ordering identity is present even for an EMPTY inventory — so an authoritative
// "node holds nothing" report can be ordered against a newer report and cannot clear it, and its
// order can be restored on rehydration. This is the durable unit; the artifacts key value and the
// changelog Payload are both the marshaled envelope.
type NodeArtifactSnapshot struct {
	NodeID string `json:"node_id"`
	Fence  int64  `json:"fence"`
	Seq    int64  `json:"seq"`
	// Ready replicates the artifact-readiness cordon across HA peers and restarts: a published envelope
	// is an accepted VERSIONED complete inventory from the owning connection, so Ready=true, and peers
	// applying it (or an instance rehydrating it) mark the node artifact-ready. Without it a non-owner
	// Foghorn holds the correct inventory but permanently cordons the node from artifact routing.
	Ready     bool                 `json:"ready"`
	Artifacts []*NodeArtifactState `json:"artifacts"`
}

// setNodeArtifactsFenced ATOMICALLY, in ONE Lua operation: compares the report's (fence, seq) against
// the node's stored watermark and, ONLY when it strictly wins, writes the envelope value (KEYS[1]),
// advances the watermark (KEYS[2]), AND appends the changelog entry (XADD KEYS[3]). Doing all three
// in one script closes the CAS-then-publish gap where a stalled old owner could win the CAS and later
// publish a stale (empty) snapshot after a newer owner. A positive fence is required (return -1
// otherwise). seq==0 is the fenced TAKEOVER MARKER (a new owner's Ready=false cordon before its first
// report): it wins over any lower fence and loses to a real report (seq>=1) of the same fence, so a
// reconnect re-arms the readiness cordon on every replica. Returns 1 applied, 0 stale no-op, -1
// unfenced. ARGV: [envelope, fence, seq, changelogEntry, maxLen].
var setNodeArtifactsFenced = goredis.NewScript(`
local fence = tonumber(ARGV[2])
local seq = tonumber(ARGV[3])
if fence == nil or seq == nil or fence <= 0 or seq < 0 then
  return -1
end
local cur = redis.call('get', KEYS[2])
if cur then
  local sep = string.find(cur, '-')
  if sep then
    local cf = tonumber(string.sub(cur, 1, sep - 1))
    local cs = tonumber(string.sub(cur, sep + 1))
    if cf ~= nil and cs ~= nil then
      if cf > fence or (cf == fence and cs >= seq) then
        return 0
      end
    end
  end
end
redis.call('set', KEYS[1], ARGV[1])
redis.call('set', KEYS[2], fence .. '-' .. seq)
redis.call('xadd', KEYS[3], 'MAXLEN', '~', ARGV[5], '*', 'data', ARGV[4])
return 1
`)

func (r *RedisStateStore) keyArtifactWatermark(nodeID string) string {
	return fmt.Sprintf("{%s}:artifacts_wm:%s", r.clusterID, nodeID)
}

// ErrUnversionedArtifactWrite means a fenced write was attempted without a positive (fence, seq); the
// fenced Redis path has no unversioned bypass.
var ErrUnversionedArtifactWrite = errors.New("fenced artifact write requires a positive (fence, seq)")

// SetNodeArtifactsFenced atomically writes the envelope + watermark + changelog entry under the
// (fence, seq) CAS. envelope is the marshaled NodeArtifactSnapshot (the Redis value); changelogEntry
// is the marshaled StateChange (the XADD payload) — the SAME envelope wrapped as a StateChange so
// peers apply it with its ordering identity. applied is false when a newer report already won.
func (r *RedisStateStore) SetNodeArtifactsFenced(ctx context.Context, nodeID string, envelope, changelogEntry []byte, fence, seq int64) (applied bool, err error) {
	if fence <= 0 || seq < 0 { // seq==0 is the takeover marker; a real report is seq>=1
		return false, ErrUnversionedArtifactWrite
	}
	res, err := setNodeArtifactsFenced.Run(ctx, r.client,
		[]string{r.keyArtifact(nodeID), r.keyArtifactWatermark(nodeID), r.changelog.Key()},
		envelope, fence, seq, changelogEntry, r.changelog.MaxLen(),
	).Int64()
	if err != nil {
		return false, err
	}
	if res == -1 {
		return false, ErrUnversionedArtifactWrite
	}
	return res == 1, nil
}

// GetAllNodeArtifacts returns each node's stored report envelope (with its ordering identity), for
// rehydration — so the accepted (fence, seq) watermark is restored alongside the artifacts.
func (r *RedisStateStore) GetAllNodeArtifacts() (map[string]*NodeArtifactSnapshot, error) {
	return scanRedisMap(r, "{"+r.clusterID+"}:artifacts:*", func(data string) (*NodeArtifactSnapshot, string, error) {
		var env NodeArtifactSnapshot
		if err := json.Unmarshal([]byte(data), &env); err != nil {
			return nil, "", err
		}
		return &env, env.NodeID, nil
	})
}

// Connection ownership for HA relay: tracks which Foghorn instance holds each node's control stream.
// Value is "instanceID|grpcAddr|fence" so the relay can look up the owner and its fence in a single GET.

func (r *RedisStateStore) keyConnOwner(nodeID string) string {
	return fmt.Sprintf("{%s}:conn_owner:%s", r.clusterID, nodeID)
}

func (r *RedisStateStore) keyPendingDVRStop(internalName string) string {
	return fmt.Sprintf("{%s}:pending_dvr_stop:%s", r.clusterID, internalName)
}

// ConnOwner is the compound value stored in the conn_owner Redis key.
type ConnOwner struct {
	InstanceID string
	GRPCAddr   string
	// Fence is the monotonic control-connection ownership fence (issued by Foghorn at Register from a
	// Postgres sequence). A reconnect ranks strictly higher, so acquisition is a fenced CAS and a
	// superseded owner loses. Zero for a legacy value written before fencing (treated as lowest).
	Fence int64
}

func encodeConnOwner(instanceID, grpcAddr string, fence int64) string {
	return fmt.Sprintf("%s|%s|%d", instanceID, grpcAddr, fence)
}

func decodeConnOwner(val string) ConnOwner {
	parts := strings.SplitN(val, "|", 3)
	switch len(parts) {
	case 0:
		return ConnOwner{}
	case 1:
		return ConnOwner{InstanceID: parts[0]}
	case 2:
		return ConnOwner{InstanceID: parts[0], GRPCAddr: parts[1]}
	default:
		owner := ConnOwner{InstanceID: parts[0], GRPCAddr: parts[1]}
		if f, err := strconv.ParseInt(parts[2], 10, 64); err == nil {
			owner.Fence = f
		}
		return owner
	}
}

// acquireConnOwnerFenced sets the conn_owner key to the caller iff there is no current owner OR the
// caller's fence is strictly higher than the stored owner's fence. Returns 1 on acquire, 0 when a
// higher-or-equal fence already owns the node (the caller LOST and must close its stream). Refreshes
// the TTL on a same-owner re-acquire. ARGV: [value, fence, ttlMillis].
var acquireConnOwnerFenced = goredis.NewScript(`
local incoming = tonumber(ARGV[2])
local cur = redis.call('get', KEYS[1])
if cur then
  local a = string.find(cur, '|')
  local b = a and string.find(cur, '|', a + 1)
  local curFence = 0
  if b then curFence = tonumber(string.sub(cur, b + 1)) or 0 end
  if incoming == nil or incoming <= curFence then
    return 0
  end
end
redis.call('set', KEYS[1], ARGV[1], 'PX', tonumber(ARGV[3]))
return 1
`)

// refreshConnOwnerFenced renews the TTL only while this instance+fence still owns the key. Returns 1
// on renew, 0 when a higher fence has taken over (caller LOST — close the stream), -1 when the key is
// missing (expired — caller re-acquires). ARGV: [value, fence, ttlMillis].
var refreshConnOwnerFenced = goredis.NewScript(`
local cur = redis.call('get', KEYS[1])
if not cur then
  return -1
end
if cur == ARGV[1] then
  redis.call('pexpire', KEYS[1], tonumber(ARGV[3]))
  return 1
end
local incoming = tonumber(ARGV[2])
local a = string.find(cur, '|')
local b = a and string.find(cur, '|', a + 1)
local curFence = 0
if b then curFence = tonumber(string.sub(cur, b + 1)) or 0 end
if incoming ~= nil and incoming > curFence then
  redis.call('set', KEYS[1], ARGV[1], 'PX', tonumber(ARGV[3]))
  return 1
end
return 0
`)

// AcquireConnOwnerFenced performs the fenced CAS acquire. acquired is false when a higher-or-equal
// fence already owns the node — the caller must reject/close its control stream (fail closed).
func (r *RedisStateStore) AcquireConnOwnerFenced(ctx context.Context, nodeID, instanceID, grpcAddr string, fence int64) (acquired bool, err error) {
	res, err := acquireConnOwnerFenced.Run(ctx, r.client,
		[]string{r.keyConnOwner(nodeID)},
		encodeConnOwner(instanceID, grpcAddr, fence), fence, connOwnerTTL.Milliseconds(),
	).Int64()
	if err != nil {
		return false, err
	}
	return res == 1, nil
}

// ErrConnOwnerLost means a higher-fence connection took ownership; the caller must close its stream.
var ErrConnOwnerLost = errors.New("conn owner lost to a higher fence")

// RefreshConnOwnerFenced renews ownership TTL. It returns ErrConnOwnerLost when a higher fence has
// taken over (close the stream) and ErrConnOwnerMissing when the key expired (re-acquire).
func (r *RedisStateStore) RefreshConnOwnerFenced(ctx context.Context, nodeID, instanceID, grpcAddr string, fence int64) error {
	res, err := refreshConnOwnerFenced.Run(ctx, r.client,
		[]string{r.keyConnOwner(nodeID)},
		encodeConnOwner(instanceID, grpcAddr, fence), fence, connOwnerTTL.Milliseconds(),
	).Int64()
	if err != nil {
		return err
	}
	switch res {
	case 1:
		return nil
	case -1:
		return ErrConnOwnerMissing
	default:
		return ErrConnOwnerLost
	}
}

func (r *RedisStateStore) GetConnOwner(ctx context.Context, nodeID string) (ConnOwner, error) {
	val, err := r.client.Get(ctx, r.keyConnOwner(nodeID)).Result()
	if errors.Is(err, goredis.Nil) {
		return ConnOwner{}, nil
	}
	if err != nil {
		return ConnOwner{}, err
	}
	return decodeConnOwner(val), nil
}

// DeleteConnOwnerIfMatch deletes the conn_owner key only if it still holds the value for the given
// instance AND fence. Matching the fence is what prevents an old connection's disconnect from
// deleting a NEWER connection's ownership after a same-instance reconnect (higher fence).
func (r *RedisStateStore) DeleteConnOwnerIfMatch(ctx context.Context, nodeID, instanceID, grpcAddr string, fence int64) (bool, error) {
	res, err := deleteConnOwnerIfMatch.Run(ctx, r.client,
		[]string{r.keyConnOwner(nodeID)},
		encodeConnOwner(instanceID, grpcAddr, fence),
	).Int64()
	if err != nil {
		return false, err
	}
	return res == 1, nil
}

func (r *RedisStateStore) RegisterPendingDVRStop(ctx context.Context, internalName string, at time.Time) error {
	if internalName == "" {
		return nil
	}
	if at.IsZero() {
		at = time.Now()
	}
	return r.client.Set(ctx, r.keyPendingDVRStop(internalName), at.UTC().Format(time.RFC3339Nano), pendingDVRStopTTL).Err()
}

func (r *RedisStateStore) ConsumePendingDVRStop(ctx context.Context, internalName string) (bool, error) {
	if internalName == "" {
		return false, nil
	}
	val, err := getAndDelete.Run(ctx, r.client, []string{r.keyPendingDVRStop(internalName)}).Result()
	if errors.Is(err, goredis.Nil) || val == nil {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// PublishStateChange appends the change to the cluster's ordered changelog
// and returns the server-assigned entry ID — the change's logical version.
func (r *RedisStateStore) PublishStateChange(change StateChange) (string, error) {
	return r.changelog.Append(context.Background(), change)
}

// ChangelogTail returns the current end of the changelog; reading from it
// yields only changes appended afterwards.
func (r *RedisStateStore) ChangelogTail(ctx context.Context) (string, error) {
	return r.changelog.Tail(ctx)
}

// ReadStateChanges consumes changes after fromID in order until ctx is done.
func (r *RedisStateStore) ReadStateChanges(ctx context.Context, fromID string, handler func(id string, change StateChange)) error {
	return r.changelog.Read(ctx, fromID, handler)
}

type redisScanner[T any] func(data string) (T, string, error)

func scanRedisMap[T any](r *RedisStateStore, pattern string, parser redisScanner[T]) (map[string]T, error) {
	ctx := context.Background()
	cursor := uint64(0)
	result := make(map[string]T)

	for {
		keys, next, err := r.client.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return nil, err
		}

		for _, key := range keys {
			value, err := r.client.Get(ctx, key).Result()
			if err != nil {
				if stateLogger != nil {
					stateLogger.WithError(err).WithField("key", key).Warn("Failed to GET redis key during scan")
				}
				continue
			}
			parsed, resultKey, err := parser(value)
			if err != nil {
				if stateLogger != nil {
					stateLogger.WithError(err).WithField("key", key).Warn("Failed to parse redis value during scan")
				}
				continue
			}
			if resultKey == "" {
				continue
			}
			result[resultKey] = parsed
		}

		cursor = next
		if cursor == 0 {
			break
		}
	}

	return result, nil
}
