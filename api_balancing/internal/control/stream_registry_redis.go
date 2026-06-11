package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

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
// is written or invalidated. Peers apply the change to their local
// in-memory copy without re-querying Commodore/SQL. Ordering and staleness
// come from the changelog's server-assigned entry IDs (see
// pkgredis.Changelog), not from any field in the payload.
type RegistryChange struct {
	InstanceID string            `json:"instance_id"`
	Entity     RegistryEntity    `json:"entity"`
	Operation  RegistryOperation `json:"operation"`
	Key        string            `json:"key"` // internal_name for sources, artifact_hash for artifacts
	Payload    json.RawMessage   `json:"payload,omitempty"`
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

// SetSource persists a source entry. internalName must be non-empty.
func (r *RedisRegistryStore) SetSource(entry StreamEntry) error {
	if entry.InternalName == "" {
		return errors.New("registry redis: source entry has empty internal_name")
	}
	payload, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	return r.client.Set(context.Background(), r.keySource(entry.InternalName), payload, 0).Err()
}

// DeleteSource drops a source entry.
func (r *RedisRegistryStore) DeleteSource(internalName string) error {
	if internalName == "" {
		return nil
	}
	return r.client.Del(context.Background(), r.keySource(internalName)).Err()
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
