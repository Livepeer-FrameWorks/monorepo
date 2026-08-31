package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/streamident"
)

const (
	ingestGenerationStoreVersion  = 2
	ingestGenerationTombstoneTTL  = 24 * time.Hour
	maxIngestGenerationTombstones = 4096
	maxActiveIngestGenerations    = streamident.MaxPublishersPerFoghorn
)

type IngestGenerationRecord struct {
	Version      int    `json:"version"`
	RuntimeName  string `json:"runtime_name"`
	Generation   string `json:"generation"`
	ConnectorPID int64  `json:"connector_pid"`
	Active       bool   `json:"active"`
	UpdatedAt    int64  `json:"updated_at_unix_milli"`
}

// IngestGenerationStore persists one atomic record per Mist runtime. Active entries survive
// indefinitely so a Helmsman restart does not lose fences for publishers that outlive it. Ended
// entries are tombstones retained for reordered commands, then age/cap evicted.
type IngestGenerationStore struct {
	dir          string
	mu           sync.RWMutex
	records      map[string]IngestGenerationRecord
	active       int
	maxActive    int
	runtimeLocks [256]sync.Mutex
	pruneHook    func(time.Time) ([]string, error)
	maintenance  struct {
		sync.Mutex
		running   bool
		requested bool
	}
}

func DefaultIngestGenerationStorePath() string {
	if path := strings.TrimSpace(os.Getenv("FRAMEWORKS_INGEST_GENERATION_STORE_PATH")); path != "" {
		return path
	}
	if stateDir := strings.TrimSpace(os.Getenv("HELMSMAN_STATE_DIR")); stateDir != "" {
		return filepath.Join(stateDir, "ingest-generation-fences")
	}
	return ""
}

func NewIngestGenerationStore(dir string) (*IngestGenerationStore, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, errors.New("ingest generation store path is empty")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create ingest generation store directory: %w", err)
	}
	store := &IngestGenerationStore{
		dir:       dir,
		records:   make(map[string]IngestGenerationRecord),
		maxActive: maxActiveIngestGenerations,
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *IngestGenerationStore) Load() (map[string]IngestGenerationRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneIngestGenerationRecords(s.records), nil
}

func (s *IngestGenerationStore) Put(runtimeName, generation string, connectorPID int64) error {
	runtimeName = strings.TrimSpace(runtimeName)
	generation = strings.TrimSpace(generation)
	if runtimeName == "" || generation == "" || connectorPID <= 0 {
		return errors.New("ingest generation record requires runtime, generation, and positive connector PID")
	}

	runtimeLock := s.runtimeLock(runtimeName)
	runtimeLock.Lock()
	defer runtimeLock.Unlock()
	s.mu.Lock()
	previous, existed := s.records[runtimeName]
	if existed && previous.Active && previous.Generation == generation && previous.ConnectorPID == connectorPID {
		// A blocking-trigger replay is the same admission, not a new grace
		// period. Preserve the original timestamp and avoid a redundant disk
		// replacement so repeated delivery cannot postpone runtime reconciliation.
		s.mu.Unlock()
		return nil
	}
	if (!existed || !previous.Active) && s.active >= s.maxActive {
		s.mu.Unlock()
		return fmt.Errorf("active ingest generation limit %d reached", s.maxActive)
	}
	reservedActive := !existed || !previous.Active
	if reservedActive {
		s.active++
	}
	s.mu.Unlock()
	record := IngestGenerationRecord{
		Version:      ingestGenerationStoreVersion,
		RuntimeName:  runtimeName,
		Generation:   generation,
		ConnectorPID: connectorPID,
		Active:       true,
		UpdatedAt:    time.Now().UnixMilli(),
	}
	if err := s.writeRecord(record); err != nil {
		if reservedActive {
			s.mu.Lock()
			s.active--
			s.mu.Unlock()
		}
		return err
	}
	s.mu.Lock()
	s.records[runtimeName] = record
	s.mu.Unlock()
	return nil
}

// MarkEnded persists only the generation's primary ended transition. Tombstone maintenance is
// scheduled separately so its global scan cannot delay close acknowledgement or fence updates.
func (s *IngestGenerationStore) MarkEnded(runtimeName, generation string, connectorPID int64) error {
	runtimeName = strings.TrimSpace(runtimeName)
	generation = strings.TrimSpace(generation)
	if runtimeName == "" || generation == "" || connectorPID <= 0 {
		return errors.New("ended ingest generation requires runtime, generation, and positive connector PID")
	}

	runtimeLock := s.runtimeLock(runtimeName)
	runtimeLock.Lock()
	s.mu.RLock()
	current, ok := s.records[runtimeName]
	s.mu.RUnlock()
	if !ok || current.Generation != generation || current.ConnectorPID != connectorPID {
		runtimeLock.Unlock()
		return nil
	}
	if current.Active {
		current.Active = false
		current.UpdatedAt = time.Now().UnixMilli()
		if err := s.writeRecord(current); err != nil {
			runtimeLock.Unlock()
			return err
		}
		s.mu.Lock()
		s.records[runtimeName] = current
		s.active--
		s.mu.Unlock()
	}
	runtimeLock.Unlock()
	return nil
}

const (
	ingestGenerationMaintenanceDelay    = 100 * time.Millisecond
	ingestGenerationMaintenanceMaxDelay = 5 * time.Second
)

// SchedulePrune coalesces close-path maintenance into one asynchronous pass. Requests arriving
// while a scan is running trigger at most one follow-up pass, so a burst of publisher closes cannot
// start one full-map scan per generation. A failed pass retries independently with capped
// exponential backoff; tombstone bounds therefore do not depend on a later publisher close.
func (s *IngestGenerationStore) SchedulePrune(onComplete func([]string, error)) {
	s.maintenance.Lock()
	if s.maintenance.running {
		s.maintenance.requested = true
		s.maintenance.Unlock()
		return
	}
	s.maintenance.running = true
	s.maintenance.Unlock()

	go func() {
		delay := ingestGenerationMaintenanceDelay
		for {
			time.Sleep(delay)
			s.maintenance.Lock()
			s.maintenance.requested = false
			s.maintenance.Unlock()

			prune := s.prune
			if s.pruneHook != nil {
				prune = s.pruneHook
			}
			evicted, err := prune(time.Now())
			if onComplete != nil {
				onComplete(evicted, err)
			}

			s.maintenance.Lock()
			if err != nil {
				delay = min(delay*2, ingestGenerationMaintenanceMaxDelay)
				s.maintenance.Unlock()
				continue
			}
			if s.maintenance.requested {
				delay = ingestGenerationMaintenanceDelay
				s.maintenance.Unlock()
				continue
			}
			s.maintenance.running = false
			s.maintenance.Unlock()
			return
		}
	}()
}

func (s *IngestGenerationStore) load() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return fmt.Errorf("read ingest generation store directory: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		raw, readErr := os.ReadFile(filepath.Join(s.dir, entry.Name()))
		if readErr != nil {
			return fmt.Errorf("read ingest generation record %s: %w", entry.Name(), readErr)
		}
		var record IngestGenerationRecord
		if decodeErr := json.Unmarshal(raw, &record); decodeErr != nil {
			return fmt.Errorf("decode ingest generation record %s: %w", entry.Name(), decodeErr)
		}
		record.RuntimeName = strings.TrimSpace(record.RuntimeName)
		record.Generation = strings.TrimSpace(record.Generation)
		if record.Version != ingestGenerationStoreVersion || record.RuntimeName == "" || record.Generation == "" || record.ConnectorPID <= 0 {
			return fmt.Errorf("invalid ingest generation record %s", entry.Name())
		}
		if existing, ok := s.records[record.RuntimeName]; ok && existing.UpdatedAt >= record.UpdatedAt {
			continue
		}
		s.records[record.RuntimeName] = record
	}
	for _, record := range s.records {
		if record.Active {
			s.active++
		}
	}
	if s.active > s.maxActive {
		return fmt.Errorf("active ingest generation count %d exceeds limit %d", s.active, s.maxActive)
	}
	_, err = s.prune(time.Now())
	return err
}

func (s *IngestGenerationStore) writeRecord(record IngestGenerationRecord) error {
	raw, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode ingest generation record: %w", err)
	}
	path := s.recordPath(record.RuntimeName)
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open ingest generation record temp file: %w", err)
	}
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("write ingest generation record: %w", err)
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close ingest generation record: %w", closeErr)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace ingest generation record: %w", err)
	}
	if err := syncDir(s.dir); err != nil {
		return fmt.Errorf("sync ingest generation store directory: %w", err)
	}
	return nil
}

func (s *IngestGenerationStore) prune(now time.Time) ([]string, error) {
	s.mu.RLock()
	tombstones := make([]IngestGenerationRecord, 0)
	for _, record := range s.records {
		if !record.Active {
			tombstones = append(tombstones, record)
		}
	}
	s.mu.RUnlock()
	sort.Slice(tombstones, func(i, j int) bool { return tombstones[i].UpdatedAt > tombstones[j].UpdatedAt })
	evicted := make([]string, 0)
	for index, record := range tombstones {
		expired := record.UpdatedAt <= 0 || now.Sub(time.UnixMilli(record.UpdatedAt)) > ingestGenerationTombstoneTTL
		if !expired && index < maxIngestGenerationTombstones {
			continue
		}
		runtimeLock := s.runtimeLock(record.RuntimeName)
		runtimeLock.Lock()
		s.mu.RLock()
		current, retained := s.records[record.RuntimeName]
		s.mu.RUnlock()
		if !retained || current.Active || current.UpdatedAt != record.UpdatedAt {
			runtimeLock.Unlock()
			continue
		}
		if err := os.Remove(s.recordPath(record.RuntimeName)); err != nil && !errors.Is(err, os.ErrNotExist) {
			runtimeLock.Unlock()
			return evicted, fmt.Errorf("remove ingest generation tombstone: %w", err)
		}
		s.mu.Lock()
		delete(s.records, record.RuntimeName)
		s.mu.Unlock()
		runtimeLock.Unlock()
		evicted = append(evicted, record.RuntimeName)
	}
	if len(evicted) > 0 {
		if err := syncDir(s.dir); err != nil {
			return evicted, fmt.Errorf("sync ingest generation store directory: %w", err)
		}
	}
	return evicted, nil
}

func (s *IngestGenerationStore) runtimeLock(runtimeName string) *sync.Mutex {
	digest := sha256.Sum256([]byte(runtimeName))
	return &s.runtimeLocks[digest[0]]
}

func (s *IngestGenerationStore) recordPath(runtimeName string) string {
	digest := sha256.Sum256([]byte(runtimeName))
	return filepath.Join(s.dir, hex.EncodeToString(digest[:])+".json")
}

func cloneIngestGenerationRecords(records map[string]IngestGenerationRecord) map[string]IngestGenerationRecord {
	cloned := make(map[string]IngestGenerationRecord, len(records))
	for runtimeName, record := range records {
		cloned[runtimeName] = record
	}
	return cloned
}
