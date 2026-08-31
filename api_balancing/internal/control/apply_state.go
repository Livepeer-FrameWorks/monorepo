package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sync"

	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/proto"
)

// seedVersionCounter is the test/bootstrap fallback when the durable Foghorn
// database is not wired. Production versions are allocated in
// foghorn.node_config_seeds and therefore stay monotonic across restarts and
// HA replicas.
type seedVersionCounter struct {
	mu  sync.Mutex
	cur map[string]uint64
}

func newSeedVersionCounter() *seedVersionCounter {
	return &seedVersionCounter{cur: make(map[string]uint64)}
}

func (c *seedVersionCounter) next(nodeID string) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cur[nodeID]++
	return c.cur[nodeID]
}

func (c *seedVersionCounter) observe(nodeID string, version uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if version > c.cur[nodeID] {
		c.cur[nodeID] = version
	}
}

func (c *seedVersionCounter) current(nodeID string) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cur[nodeID]
}

var (
	seedVersions = newSeedVersionCounter()
)

func observeAppliedSeedVersion(ctx context.Context, nodeID string, version uint64) error {
	if version == 0 {
		return nil
	}
	// Leave enough counter space that a malformed authenticated node cannot
	// advance its own durable row to the next-increment overflow boundary.
	if version > uint64(math.MaxInt64/2) {
		return errors.New("applied ConfigSeed version exceeds the durable counter range")
	}
	seedVersions.observe(nodeID, version)
	if db == nil {
		return nil
	}
	return foghorndb.New(db).EnsureConfigSeedVersionAtLeast(ctx, foghorndb.EnsureConfigSeedVersionAtLeastParams{
		NodeID: nodeID, VersionCounter: int64(version),
	})
}

func canRetainAppliedConfigSeed(fingerprintResolved bool, appliedVersion uint64) bool {
	return fingerprintResolved && appliedVersion > 0 && appliedVersion <= uint64(math.MaxInt64/2)
}

// allocateAndPersistConfigSeed pairs the version allocation and exact payload
// under one row lock. No concurrent capability refresh can publish or consume
// an allocated version before its seed is durable.
func allocateAndPersistConfigSeed(ctx context.Context, nodeID string, seed *ipcpb.ConfigSeed) (uint64, error) {
	if seed == nil || nodeID == "" || seed.GetNodeId() != nodeID {
		return 0, errors.New("ConfigSeed allocation requires matching node identity")
	}
	if db == nil {
		version := seedVersions.next(nodeID)
		seed.SeedVersion = version
		return version, nil
	}

	persisted, err := prepareAndPersistConfigSeed(ctx, nodeID, func(_ *ipcpb.ConfigSeed) (*ipcpb.ConfigSeed, error) {
		return proto.CloneOf(seed), nil
	})
	if err != nil {
		return 0, err
	}
	proto.Reset(seed)
	proto.Merge(seed, persisted)
	return seed.GetSeedVersion(), nil
}

// prepareAndPersistConfigSeed locks the node's version row, loads the latest
// durable seed under that same lock, and lets the producer apply its mutation
// to that exact base before allocating the published payload. This keeps
// partial producers (capability rotation and outage fallback composition) from
// overwriting fields committed by a concurrent full-seed producer.
func prepareAndPersistConfigSeed(ctx context.Context, nodeID string, prepare func(*ipcpb.ConfigSeed) (*ipcpb.ConfigSeed, error)) (*ipcpb.ConfigSeed, error) {
	if nodeID == "" || prepare == nil {
		return nil, errors.New("ConfigSeed preparation requires node identity and producer")
	}
	if db == nil {
		candidate, err := prepare(nil)
		if err != nil {
			return nil, err
		}
		if candidate == nil || candidate.GetNodeId() != nodeID {
			return nil, errors.New("ConfigSeed producer returned mismatched node identity")
		}
		candidate.SeedVersion = seedVersions.next(nodeID)
		return candidate, nil
	}

	var committed *ipcpb.ConfigSeed
	err := database.WithRetryablePostgresTx(ctx, db, nil, func(tx *sql.Tx) error {
		queries := foghorndb.New(tx)
		if floor := seedVersions.current(nodeID); floor > 0 {
			if err := queries.EnsureConfigSeedVersionAtLeast(ctx, foghorndb.EnsureConfigSeedVersionAtLeastParams{
				NodeID: nodeID, VersionCounter: int64(floor),
			}); err != nil {
				return fmt.Errorf("restore ConfigSeed version floor: %w", err)
			}
		}
		version, err := queries.AllocateConfigSeedVersion(ctx, nodeID)
		if err != nil {
			return fmt.Errorf("allocate ConfigSeed version: %w", err)
		}
		if version <= 0 {
			return errors.New("allocated ConfigSeed version is not positive")
		}
		// Remember the allocated floor before any later payload work can fail.
		// The transaction may roll back, but a skipped version is harmless while
		// sending a lower fallback version would be rejected by Helmsman.
		seedVersions.observe(nodeID, uint64(version))
		var latest *ipcpb.ConfigSeed
		row, loadErr := queries.GetLastConfigSeed(ctx, nodeID)
		switch {
		case loadErr == nil:
			latest = &ipcpb.ConfigSeed{}
			if decodeErr := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(row.SeedPayload, latest); decodeErr != nil {
				return fmt.Errorf("decode persisted ConfigSeed: %w", decodeErr)
			}
			if latest.GetNodeId() != nodeID || int64(latest.GetSeedVersion()) != row.SeedVersion {
				return errors.New("persisted ConfigSeed identity/version mismatch")
			}
		case errors.Is(loadErr, sql.ErrNoRows):
		default:
			return fmt.Errorf("load latest ConfigSeed under lock: %w", loadErr)
		}
		candidate, err := prepare(latest)
		if err != nil {
			return err
		}
		if candidate == nil || candidate.GetNodeId() != nodeID {
			return errors.New("ConfigSeed producer returned mismatched node identity")
		}
		candidate = proto.CloneOf(candidate)
		candidate.SeedVersion = uint64(version)
		encoded, err := proto.MarshalOptions{Deterministic: true}.Marshal(candidate)
		if err != nil {
			return fmt.Errorf("encode ConfigSeed: %w", err)
		}
		rows, err := queries.PersistConfigSeed(ctx, foghorndb.PersistConfigSeedParams{
			NodeID: nodeID, SeedVersion: version, SeedPayload: encoded,
		})
		if err != nil {
			return fmt.Errorf("persist ConfigSeed: %w", err)
		}
		if rows != 1 {
			return fmt.Errorf("persist allocated ConfigSeed version %d: rows=%d", version, rows)
		}
		committed = candidate
		return nil
	})
	if err != nil {
		return nil, err
	}
	return committed, nil
}

func loadLastConfigSeed(ctx context.Context, nodeID string) (*ipcpb.ConfigSeed, error) {
	if db == nil || nodeID == "" {
		return nil, nil
	}
	row, err := foghorndb.New(db).GetLastConfigSeed(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	seed := &ipcpb.ConfigSeed{}
	if err := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(row.SeedPayload, seed); err != nil {
		return nil, fmt.Errorf("decode persisted ConfigSeed: %w", err)
	}
	if seed.GetNodeId() != nodeID || int64(seed.GetSeedVersion()) != row.SeedVersion {
		return nil, errors.New("persisted ConfigSeed identity/version mismatch")
	}
	return seed, nil
}
