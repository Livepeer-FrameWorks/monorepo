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

// RegistryOperation distinguishes upsert/delete on pubsub.
type RegistryOperation string

const (
	RegistryOpUpsert RegistryOperation = "upsert"
	RegistryOpDelete RegistryOperation = "delete"
)

// RegistryChange is the pubsub payload published whenever a registry entry
// is written or invalidated. Subscribers apply the change to their local
// in-memory copy without re-querying Commodore/SQL.
//
// PublishedAtUnixNano is a monotonic stamp used by subscribers to drop
// stale-snapshot races where a slower publisher's older entry could
// otherwise clobber a newer local mutation (sub-second pubsub queue lag
// + concurrent local writes). Sources have their own per-Location stamp
// (Location.UpdatedAt) and rely on that; artifacts have no per-Location
// timestamp and rely on PublishedAtUnixNano instead.
type RegistryChange struct {
	InstanceID          string            `json:"instance_id"`
	Entity              RegistryEntity    `json:"entity"`
	Operation           RegistryOperation `json:"operation"`
	Key                 string            `json:"key"` // internal_name for sources, artifact_hash for artifacts
	Payload             json.RawMessage   `json:"payload,omitempty"`
	PublishedAtUnixNano int64             `json:"published_at_unix_nano,omitempty"`
}

// RedisRegistryStore persists StreamRegistry state to Redis with write-
// through semantics matching the state-package store. Keys are hash-tag-
// prefixed by cluster so a multi-cluster Redis cluster slot-routes
// correctly.
type RedisRegistryStore struct {
	client    goredis.UniversalClient
	pubsub    *pkgredis.TypedPubSub[RegistryChange]
	clusterID string
	channel   string
}

// NewRedisRegistryStore constructs a Redis-backed store for the given
// Foghorn cluster. The client must already be configured and connected.
func NewRedisRegistryStore(client goredis.UniversalClient, clusterID string) *RedisRegistryStore {
	return &RedisRegistryStore{
		client:    client,
		pubsub:    pkgredis.NewTypedPubSub[RegistryChange](client),
		clusterID: clusterID,
		channel:   fmt.Sprintf("foghorn:%s:registry_updates", clusterID),
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

// Publish broadcasts a registry change to peer Foghorn instances.
func (r *RedisRegistryStore) Publish(change RegistryChange) error {
	return r.pubsub.Publish(context.Background(), r.channel, change)
}

// Subscribe streams registry changes from peer Foghorn instances. The
// caller filters self-originating messages by InstanceID.
func (r *RedisRegistryStore) Subscribe(ctx context.Context, handler func(RegistryChange)) error {
	return r.pubsub.Subscribe(ctx, r.channel, handler)
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
