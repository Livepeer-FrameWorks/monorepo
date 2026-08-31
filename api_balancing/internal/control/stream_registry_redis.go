package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	pkgredis "github.com/Livepeer-FrameWorks/monorepo/pkg/redis"
	goredis "github.com/redis/go-redis/v9"
)

// RegistryEntity is the kind of object a published RegistryChange refers to.
type RegistryEntity string

const (
	RegistryEntitySource   RegistryEntity = "source"
	RegistryEntityArtifact RegistryEntity = "artifact"
)

// RegistryOperation distinguishes upsert/delete on the changelog.
type RegistryOperation string

const (
	RegistryOpUpsert RegistryOperation = "upsert"
	RegistryOpDelete RegistryOperation = "delete"
)

// RegistryChange is the changelog entry appended whenever a registry entry
// is written or invalidated. Peers apply the change to their local in-memory copy without
// re-querying Commodore/SQL. Artifact changes follow changelog order; source ownership follows the
// monotonic source revision so delayed upserts and tombstones cannot override a newer transition.
type RegistryChange struct {
	InstanceID string            `json:"instance_id"`
	Entity     RegistryEntity    `json:"entity"`
	Operation  RegistryOperation `json:"operation"`
	Key        string            `json:"key"` // internal_name for sources, artifact_hash for artifacts
	Payload    json.RawMessage   `json:"payload,omitempty"`
	// SourceRevision orders source upserts and tombstones independently of Redis stream IDs.
	SourceRevision int64 `json:"source_revision,omitempty"`
}

// registryChangelogMaxLen bounds the registry changelog. Sized like the
// state changelog: the retained window must comfortably cover an instance's
// worst-case downtime, after which key rehydration covers the rest.
const registryChangelogMaxLen = 100000

// RedisRegistryStore persists StreamRegistry state to Redis with write-
// through semantics matching the state-package store. Keys are hash-tag-
// prefixed by cluster so a multi-cluster Redis cluster slot-routes
// correctly.
type RedisRegistryStore struct {
	client    goredis.UniversalClient
	changelog *pkgredis.Changelog[RegistryChange]
	clusterID string
}

// Source writes and deletes use one Redis-side revision CAS that also appends the changelog entry.
// The watermark survives deletion, so a delayed stale upsert or delete cannot resurrect or remove a
// newer source after a reader restart or changelog-retention gap.
var setSourceRevisioned = goredis.NewScript(`
local rev = tonumber(ARGV[2])
if rev == nil or rev < 0 then return -1 end
local cur = tonumber(redis.call('get', KEYS[2]) or '0')
if cur > rev or (rev == 0 and cur > 0) then return 0 end
redis.call('set', KEYS[1], ARGV[1])
if rev > cur then redis.call('set', KEYS[2], ARGV[5]) end
redis.call('xadd', KEYS[3], 'MAXLEN', '~', ARGV[4], '*', 'data', ARGV[3])
return 1
`)

var deleteSourceRevisioned = goredis.NewScript(`
local rev = tonumber(ARGV[1])
if rev == nil or rev < 0 then return -1 end
local cur = tonumber(redis.call('get', KEYS[2]) or '0')
if cur > rev then return 0 end
redis.call('del', KEYS[1])
if rev > cur then redis.call('set', KEYS[2], ARGV[4]) end
redis.call('xadd', KEYS[3], 'MAXLEN', '~', ARGV[3], '*', 'data', ARGV[2])
return 1
`)

// NewRedisRegistryStore constructs a Redis-backed store for the given
// Foghorn cluster. The client must already be configured and connected.
func NewRedisRegistryStore(client goredis.UniversalClient, clusterID string) *RedisRegistryStore {
	return &RedisRegistryStore{
		client:    client,
		changelog: pkgredis.NewChangelog[RegistryChange](client, fmt.Sprintf("{%s}:registry_changelog", clusterID), registryChangelogMaxLen),
		clusterID: clusterID,
	}
}

func (r *RedisRegistryStore) keySource(internalName string) string {
	return fmt.Sprintf("{%s}:registry:source:%s", r.clusterID, internalName)
}

func (r *RedisRegistryStore) keyArtifact(hash string) string {
	return fmt.Sprintf("{%s}:registry:artifact:%s", r.clusterID, hash)
}

func (r *RedisRegistryStore) keySourceRevision(internalName string) string {
	return fmt.Sprintf("{%s}:registry:source_revision:%s", r.clusterID, internalName)
}

// SourceRevision returns the durable per-stream watermark, including when a
// revisioned delete removed the source payload. Repair code uses the watermark
// to allocate above every transition Redis has observed.
func (r *RedisRegistryStore) SourceRevision(ctx context.Context, internalName string) (int64, error) {
	value, err := r.client.Get(ctx, r.keySourceRevision(internalName)).Result()
	if errors.Is(err, goredis.Nil) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	revision, err := strconv.ParseInt(value, 10, 64)
	if err != nil || revision < 0 {
		return 0, fmt.Errorf("registry redis: invalid source revision %q", value)
	}
	return revision, nil
}

// SetSourceRevisioned atomically persists a source snapshot and publishes its changelog entry when
// revision is not older than the stored watermark. Equal revisions are idempotent retries. Revision
// zero is accepted only while the key has never carried a versioned push-source transition.
func (r *RedisRegistryStore) SetSourceRevisioned(ctx context.Context, entry StreamEntry, change RegistryChange, revision int64) (bool, error) {
	if entry.InternalName == "" {
		return false, errors.New("registry redis: source entry has empty internal_name")
	}
	payload, err := json.Marshal(entry)
	if err != nil {
		return false, err
	}
	changePayload, err := json.Marshal(change)
	if err != nil {
		return false, err
	}
	result, err := setSourceRevisioned.Run(ctx, r.client,
		[]string{r.keySource(entry.InternalName), r.keySourceRevision(entry.InternalName), r.changelog.Key()},
		payload, revision, changePayload, r.changelog.MaxLen(), strconv.FormatInt(revision, 10),
	).Int64()
	if err != nil {
		return false, err
	}
	if result < 0 {
		return false, fmt.Errorf("registry redis: invalid source revision %d", revision)
	}
	return result == 1, nil
}

// DeleteSourceRevisioned atomically deletes a source and publishes a revisioned tombstone. A delete
// carrying the current watermark is accepted (delete-if-not-superseded: the deleter acted on the
// latest revision, whose state made the entry evictable); only a revision strictly below the
// watermark — a concurrent newer transition landed first — is rejected. The retained watermark
// rejects stale writes after deletion.
func (r *RedisRegistryStore) DeleteSourceRevisioned(ctx context.Context, internalName string, change RegistryChange, revision int64) (bool, error) {
	if internalName == "" {
		return true, nil
	}
	changePayload, err := json.Marshal(change)
	if err != nil {
		return false, err
	}
	result, err := deleteSourceRevisioned.Run(ctx, r.client,
		[]string{r.keySource(internalName), r.keySourceRevision(internalName), r.changelog.Key()},
		revision, changePayload, r.changelog.MaxLen(), strconv.FormatInt(revision, 10),
	).Int64()
	if err != nil {
		return false, err
	}
	if result < 0 {
		return false, fmt.Errorf("registry redis: invalid source delete revision %d", revision)
	}
	return result == 1, nil
}

// SetArtifact persists an artifact entry. artifactHash must be non-empty.
func (r *RedisRegistryStore) SetArtifact(entry ArtifactEntry) error {
	if entry.ArtifactHash == "" {
		return errors.New("registry redis: artifact entry has empty hash")
	}
	payload, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	return r.client.Set(context.Background(), r.keyArtifact(entry.ArtifactHash), payload, 0).Err()
}

// DeleteArtifact drops an artifact entry.
func (r *RedisRegistryStore) DeleteArtifact(hash string) error {
	if hash == "" {
		return nil
	}
	return r.client.Del(context.Background(), r.keyArtifact(hash)).Err()
}

// GetAllSources rehydrates every source entry on startup. Stored values
// are the JSON snapshot at write-time; live-presence and TTL fields are
// recomputed on next lookup.
func (r *RedisRegistryStore) GetAllSources() (map[string]StreamEntry, error) {
	return scanRegistryMap(r, "{"+r.clusterID+"}:registry:source:*", func(data string) (StreamEntry, string, error) {
		var e StreamEntry
		if err := json.Unmarshal([]byte(data), &e); err != nil {
			return StreamEntry{}, "", err
		}
		return e, e.InternalName, nil
	})
}

// GetSource reads the current durable snapshot for one internal stream name.
func (r *RedisRegistryStore) GetSource(ctx context.Context, internalName string) (StreamEntry, bool, error) {
	if strings.TrimSpace(internalName) == "" {
		return StreamEntry{}, false, nil
	}
	payload, err := r.client.Get(ctx, r.keySource(internalName)).Bytes()
	if errors.Is(err, goredis.Nil) {
		return StreamEntry{}, false, nil
	}
	if err != nil {
		return StreamEntry{}, false, err
	}
	var entry StreamEntry
	if err := json.Unmarshal(payload, &entry); err != nil {
		return StreamEntry{}, false, err
	}
	return entry, true, nil
}

// GetAllSourceRevisions returns the durable source watermark for every source key, including
// tombstones whose source value has been deleted. Rehydration uses these watermarks to remove stale
// in-memory entries after a changelog-retention gap.
func (r *RedisRegistryStore) GetAllSourceRevisions() (map[string]int64, error) {
	ctx := context.Background()
	prefix := "{" + r.clusterID + "}:registry:source_revision:"
	cursor := uint64(0)
	out := make(map[string]int64)
	for {
		keys, next, err := r.client.Scan(ctx, cursor, prefix+"*", 100).Result()
		if err != nil {
			return nil, err
		}
		for _, key := range keys {
			value, err := r.client.Get(ctx, key).Result()
			if errors.Is(err, goredis.Nil) {
				continue
			}
			if err != nil {
				return nil, err
			}
			revision, err := strconv.ParseInt(value, 10, 64)
			if err != nil || revision <= 0 {
				continue
			}
			internalName := strings.TrimPrefix(key, prefix)
			if internalName != "" {
				out[internalName] = revision
			}
		}
		if next == 0 {
			break
		}
		cursor = next
	}
	return out, nil
}

// GetSourceRevision returns one source's durable watermark (0 when the key has never carried a
// versioned transition). Delete publishers use it when the in-memory Location no longer records the
// revision — a revision-0 delete of a versioned source is always rejected, so the watermark itself is
// the only revision that can carry the delete-if-not-superseded tombstone.
func (r *RedisRegistryStore) GetSourceRevision(ctx context.Context, internalName string) (int64, error) {
	if internalName == "" {
		return 0, nil
	}
	value, err := r.client.Get(ctx, r.keySourceRevision(internalName)).Result()
	if errors.Is(err, goredis.Nil) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	// A corrupt/non-numeric watermark reads as unversioned (ParseInt yields 0) rather than failing
	// the delete; a negative value is equally meaningless and clamps to unversioned.
	revision, _ := strconv.ParseInt(value, 10, 64) //nolint:errcheck // corrupt watermark = unversioned
	if revision < 0 {
		return 0, nil
	}
	return revision, nil
}

// GetAllArtifacts rehydrates every artifact entry on startup.
func (r *RedisRegistryStore) GetAllArtifacts() (map[string]ArtifactEntry, error) {
	return scanRegistryMap(r, "{"+r.clusterID+"}:registry:artifact:*", func(data string) (ArtifactEntry, string, error) {
		var e ArtifactEntry
		if err := json.Unmarshal([]byte(data), &e); err != nil {
			return ArtifactEntry{}, "", err
		}
		return e, e.ArtifactHash, nil
	})
}

// Publish appends a registry change to the changelog and returns its
// server-assigned entry ID — the change's logical version.
func (r *RedisRegistryStore) Publish(change RegistryChange) (string, error) {
	return r.changelog.Append(context.Background(), change)
}

// ChangelogTail returns the newest changelog entry ID ("0-0" when empty).
// Capture it before rehydrating keys; reading from it afterwards yields
// exactly the changes not yet reflected in the key snapshot.
func (r *RedisRegistryStore) ChangelogTail(ctx context.Context) (string, error) {
	return r.changelog.Tail(ctx)
}

// ReadChanges consumes registry changes after fromID in log order until ctx
// is done. The caller filters self-originating entries by InstanceID.
func (r *RedisRegistryStore) ReadChanges(ctx context.Context, fromID string, handler func(id string, change RegistryChange)) error {
	return r.changelog.Read(ctx, fromID, handler)
}

type registryScanner[T any] func(data string) (T, string, error)

func scanRegistryMap[T any](r *RedisRegistryStore, pattern string, parser registryScanner[T]) (map[string]T, error) {
	ctx := context.Background()
	cursor := uint64(0)
	out := make(map[string]T)
	for {
		keys, next, err := r.client.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return nil, err
		}
		for _, key := range keys {
			val, err := r.client.Get(ctx, key).Result()
			if err != nil {
				if errors.Is(err, goredis.Nil) {
					continue
				}
				return nil, err
			}
			parsed, mapKey, err := parser(val)
			if err != nil {
				continue
			}
			if mapKey == "" {
				continue
			}
			out[mapKey] = parsed
		}
		if next == 0 {
			break
		}
		cursor = next
	}
	return out, nil
}
