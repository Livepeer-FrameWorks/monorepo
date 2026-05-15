package leases

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DeleterFunc executes a real delete for one AssetType/AssetHash. Returns
// (bytesDeleted, error). When the error is ErrLeaseHeld the store leaves the
// entry queued for another retry; on other errors the entry stays queued
// (it'll retry); on success the entry is forgotten.
type DeleterFunc func(assetType, assetHash string) (uint64, error)

// PendingDelete records an operator/control-plane delete intent that could
// not run immediately because a lease was held or cleanup was paused.
type PendingDelete struct {
	AssetType string    `json:"asset_type"` // "clip" | "vod" | "dvr"
	AssetHash string    `json:"asset_hash"`
	Queued    time.Time `json:"queued"`
}

// DeferredStore persists the queue to a small JSON file under StorageRoot.
// A retry loop drains entries when leases release.
type DeferredStore struct {
	mu        sync.Mutex
	path      string
	entries   map[string]PendingDelete // key = assetType + "|" + assetHash
	deleter   DeleterFunc
	onDeleted func(p PendingDelete, bytes uint64)
}

const deferredFilename = ".pending-deletes.json"

func NewDeferredStore(storageRoot string, deleter DeleterFunc, onDeleted func(PendingDelete, uint64)) *DeferredStore {
	return &DeferredStore{
		path:      filepath.Join(storageRoot, deferredFilename),
		entries:   make(map[string]PendingDelete),
		deleter:   deleter,
		onDeleted: onDeleted,
	}
}

func (s *DeferredStore) Load() error {
	if s == nil {
		return errors.New("store is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var list []PendingDelete
	if err := json.Unmarshal(data, &list); err != nil {
		return err
	}
	s.entries = make(map[string]PendingDelete, len(list))
	for _, p := range list {
		s.entries[deferredKey(p.AssetType, p.AssetHash)] = p
	}
	return nil
}

func (s *DeferredStore) Enqueue(p PendingDelete) {
	if s == nil || p.AssetHash == "" {
		return
	}
	if p.Queued.IsZero() {
		p.Queued = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[deferredKey(p.AssetType, p.AssetHash)] = p
	_ = s.persistLocked() //nolint:errcheck // persistence is best-effort; queue survives in-memory and re-persists on next mutation
}

func (s *DeferredStore) List() []PendingDelete {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]PendingDelete, 0, len(s.entries))
	for _, p := range s.entries {
		out = append(out, p)
	}
	return out
}

func (s *DeferredStore) Forget(assetType, assetHash string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, deferredKey(assetType, assetHash))
	_ = s.persistLocked() //nolint:errcheck // persistence is best-effort; queue survives in-memory and re-persists on next mutation
}

func (s *DeferredStore) Count() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

// Drain attempts to delete every pending entry. Entries whose deleter returns
// ErrLeaseHeld remain in the queue; entries that succeed are removed.
// Returns the number of successful deletes.
func (s *DeferredStore) Drain() int {
	if s == nil || s.deleter == nil {
		return 0
	}
	pending := s.List()
	successes := 0
	for _, p := range pending {
		bytes, err := s.deleter(p.AssetType, p.AssetHash)
		if err == nil {
			successes++
			s.Forget(p.AssetType, p.AssetHash)
			if s.onDeleted != nil {
				s.onDeleted(p, bytes)
			}
			continue
		}
		if errors.Is(err, ErrLeaseHeld) {
			continue // try again later
		}
		// Other errors: leave queued for next pass.
	}
	return successes
}

func (s *DeferredStore) persistLocked() error {
	if s.path == "" {
		return nil
	}
	list := make([]PendingDelete, 0, len(s.entries))
	for _, p := range s.entries {
		list = append(list, p)
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func deferredKey(assetType, assetHash string) string {
	return assetType + "|" + assetHash
}
