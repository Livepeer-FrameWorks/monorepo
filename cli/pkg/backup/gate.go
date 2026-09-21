package backup

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// MaxAge is how old a backup may be when a contract or irreversible migration uses it. It is measured from the
// moment the backup started.
const MaxAge = time.Hour

// maxClockSkew is how far in the future a backup's start may lie before the gate treats the timestamp as wrong.
const maxClockSkew = 5 * time.Minute

// ErrBackupRequired is returned when a gated migration runs without --backup.
var ErrBackupRequired = errors.New("a verified backup is required")

// GateRequest describes one gated operation: what it is, and the live ledger digest of every database it changes,
// keyed like Database.Key and ClickHouse.Key.
type GateRequest struct {
	Operation   string
	LiveDigests map[string]string
	// Cluster is the cluster the operation runs against; a backup that records a different cluster is refused.
	// Either side may be empty when the manifest source names no cluster.
	Cluster string
}

// CheckGate decides whether the backup protects the operation. It accepts only a manifest that started at most
// MaxAge before now and that records, for every database the operation changes, the ledger digest the database has
// now: no migration ran after the backup.
func CheckGate(m *Manifest, req GateRequest, now time.Time) error {
	if m == nil {
		return fmt.Errorf("%s: %w", req.Operation, ErrBackupRequired)
	}
	var problems []string
	age := now.Sub(m.CreatedAt)
	switch {
	case age > MaxAge:
		problems = append(problems, fmt.Sprintf("the backup started %s ago; the limit is %s", age.Round(time.Second), MaxAge))
	case age < -maxClockSkew:
		problems = append(problems, fmt.Sprintf("the backup's start %s is in the future; check the clock", m.CreatedAt.Format(time.RFC3339)))
	}
	if m.Cluster != "" && req.Cluster != "" && m.Cluster != req.Cluster {
		problems = append(problems, fmt.Sprintf("the backup is of cluster %q, not %q", m.Cluster, req.Cluster))
	}
	keys := make([]string, 0, len(req.LiveDigests))
	for key := range req.LiveDigests {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		recorded, ok := recordedDigest(m, key)
		if !ok {
			problems = append(problems, fmt.Sprintf("%s is not in the backup", key))
			continue
		}
		if recorded != req.LiveDigests[key] {
			problems = append(problems, fmt.Sprintf("%s: its migration ledger changed after the backup", key))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%s: the backup does not protect it: %s", req.Operation, strings.Join(problems, "; "))
	}
	return nil
}

func recordedDigest(m *Manifest, key string) (string, bool) {
	if db, ok := m.Database(key); ok {
		return db.LedgerDigest, true
	}
	if m.ClickHouse != nil && m.ClickHouse.Key() == key {
		return m.ClickHouse.LedgerDigest, true
	}
	return "", false
}

// RequireGate reads the backup at raw, verifies the files of the databases the operation changes, and applies
// CheckGate. An empty raw path refuses with ErrBackupRequired.
func RequireGate(ctx context.Context, raw string, req GateRequest, now time.Time) (*Manifest, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("%s: %w; take one with `frameworks cluster backup create --to <dir|s3-url> --all` and pass its path with --backup (it must be under %s old)",
			req.Operation, ErrBackupRequired, MaxAge)
	}
	loc, err := ParseLocation(raw)
	if err != nil {
		return nil, err
	}
	m, err := ReadManifest(ctx, loc)
	if err != nil {
		return nil, err
	}
	if err := CheckGate(m, req, now); err != nil {
		return nil, err
	}
	keys := map[string]bool{}
	for key := range req.LiveDigests {
		keys[key] = true
	}
	if err := VerifyFiles(ctx, loc, m.Files(keys)); err != nil {
		return nil, err
	}
	return m, nil
}
