// Package backup holds the engine-independent parts of cluster backup and restore: the manifest that describes one
// backup, the destinations a backup is written to (a local directory or an S3 prefix), the ledger digest, and the
// gate that contract and irreversible migrations pass through. The per-engine dump and restore commands live in
// cli/pkg/provisioner (database_backup.go and clickhouse_backup.go).
package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"sort"
	"strings"
	"time"
)

const (
	// FormatVersion is the manifest layout this CLI writes and reads.
	FormatVersion = 1
	// ManifestName is the manifest's file name inside a backup. It is written last, so a backup without it is
	// incomplete.
	ManifestName = "manifest.json"
)

// Engine names recorded per database.
const (
	EnginePostgres   = "postgres"
	EngineYugabyte   = "yugabyte"
	EngineClickHouse = "clickhouse"
)

// Section names of a PostgreSQL/YugabyteDB dump, in restore order.
var Sections = []string{"pre-data", "data", "post-data"}

// Manifest describes one backup. File paths are relative to the backup location.
type Manifest struct {
	FormatVersion int       `json:"format_version"`
	CreatedAt     time.Time `json:"created_at"`
	CompletedAt   time.Time `json:"completed_at"`
	CLIVersion    string    `json:"cli_version"`
	Cluster       string    `json:"cluster,omitempty"`

	Databases  []Database  `json:"databases,omitempty"`
	ClickHouse *ClickHouse `json:"clickhouse,omitempty"`
}

// File is one stored object with the evidence to verify it.
type File struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Section is one gzip-compressed dump section of a database.
type Section struct {
	Name string `json:"name"`
	File File   `json:"file"`
}

// DatabaseGrant is one database-level privilege, re-applied to a restored database.
type DatabaseGrant struct {
	Privilege string `json:"privilege"`
	Grantee   string `json:"grantee"`
}

// Database is one PostgreSQL/YugabyteDB database in a backup.
type Database struct {
	// Instance is empty for the cluster's primary PostgreSQL/YugabyteDB and names a manifest postgres instance
	// (for example "support") otherwise.
	Instance string `json:"instance,omitempty"`
	Name     string `json:"name"`
	// Source is the logical database whose schema the physical database carries, when it differs from Name (the
	// per-cell Foghorn databases carry `foghorn`).
	Source   string `json:"source,omitempty"`
	Engine   string `json:"engine"`
	Owner    string `json:"owner"`
	Encoding string `json:"encoding,omitempty"`
	Collate  string `json:"collate,omitempty"`
	Ctype    string `json:"ctype,omitempty"`

	Grants   []DatabaseGrant `json:"grants,omitempty"`
	Sections []Section       `json:"sections"`

	// Ledger is the _migrations content captured in the dump; LedgerDigest is LedgerDigest(Ledger).
	Ledger       []LedgerRow `json:"ledger"`
	LedgerDigest string      `json:"ledger_digest"`
	// RowCounts is the number of rows the data section loads per table, counted from the dump itself.
	RowCounts map[string]int64 `json:"row_counts"`
}

// Key identifies the database across the manifest and the live cluster.
func (d Database) Key() string { return DatabaseKey(d.Instance, d.Name) }

// DatabaseKey is the key of a PostgreSQL/YugabyteDB database: "postgres/<name>" for the primary cluster and
// "<instance>/<name>" for a named instance.
func DatabaseKey(instance, name string) string {
	if instance == "" {
		return "postgres/" + name
	}
	return instance + "/" + name
}

// ClickHouseKey is the key of a ClickHouse database.
func ClickHouseKey(database string) string { return "clickhouse/" + database }

// ClickHouse is the ClickHouse part of a backup: the authoritative tables of one database.
type ClickHouse struct {
	Database     string            `json:"database"`
	Ledger       []LedgerRow       `json:"ledger"`
	LedgerDigest string            `json:"ledger_digest"`
	Tables       []ClickHouseTable `json:"tables"`
}

// Key identifies the ClickHouse database for the gate.
func (c ClickHouse) Key() string { return ClickHouseKey(c.Database) }

// ClickHouseTable is one table dumped as gzip-compressed TabSeparated rows over Columns.
type ClickHouseTable struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Rows    int64    `json:"rows"`
	File    File     `json:"file"`
}

// LedgerRow is one migration ledger entry.
type LedgerRow struct {
	Version  string `json:"version"`
	Phase    string `json:"phase"`
	Seq      int    `json:"seq"`
	Checksum string `json:"checksum"`
}

// LedgerDigest is a SHA-256 over the ledger rows in (version, phase, seq) order. Two ledgers have the same digest
// exactly when they hold the same migrations with the same checksums, whatever order they were read in.
func LedgerDigest(rows []LedgerRow) string {
	sorted := append([]LedgerRow(nil), rows...)
	sort.Slice(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.Version != b.Version {
			return a.Version < b.Version
		}
		if a.Phase != b.Phase {
			return a.Phase < b.Phase
		}
		return a.Seq < b.Seq
	})
	h := sha256.New()
	for _, row := range sorted {
		fmt.Fprintf(h, "%s\t%s\t%d\t%s\n", row.Version, row.Phase, row.Seq, row.Checksum)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// LedgerDifference lists the rows only one side has, for the post-restore report of migrations to re-apply.
func LedgerDifference(a, b []LedgerRow) (onlyA, onlyB []LedgerRow) {
	key := func(r LedgerRow) string { return fmt.Sprintf("%s\t%s\t%d\t%s", r.Version, r.Phase, r.Seq, r.Checksum) }
	inA, inB := map[string]bool{}, map[string]bool{}
	for _, r := range a {
		inA[key(r)] = true
	}
	for _, r := range b {
		inB[key(r)] = true
	}
	for _, r := range a {
		if !inB[key(r)] {
			onlyA = append(onlyA, r)
		}
	}
	for _, r := range b {
		if !inA[key(r)] {
			onlyB = append(onlyB, r)
		}
	}
	return onlyA, onlyB
}

// Database returns the manifest entry with key, if any.
func (m *Manifest) Database(key string) (Database, bool) {
	for _, db := range m.Databases {
		if db.Key() == key {
			return db, true
		}
	}
	return Database{}, false
}

// Validate checks the manifest's internal consistency: the format version, the timestamps, every recorded ledger
// digest, and that every database has all three sections.
func (m *Manifest) Validate() error {
	var problems []string
	if m.FormatVersion != FormatVersion {
		problems = append(problems, fmt.Sprintf("format_version %d is not %d", m.FormatVersion, FormatVersion))
	}
	if m.CreatedAt.IsZero() || m.CompletedAt.IsZero() || m.CompletedAt.Before(m.CreatedAt) {
		problems = append(problems, "created_at/completed_at are missing or out of order")
	}
	if len(m.Databases) == 0 && m.ClickHouse == nil {
		problems = append(problems, "the backup holds no database")
	}
	seen := map[string]bool{}
	for _, db := range m.Databases {
		if seen[db.Key()] {
			problems = append(problems, fmt.Sprintf("%s is listed twice", db.Key()))
		}
		seen[db.Key()] = true
		if LedgerDigest(db.Ledger) != db.LedgerDigest {
			problems = append(problems, fmt.Sprintf("%s: ledger_digest does not match its ledger rows", db.Key()))
		}
		if len(db.Sections) != len(Sections) {
			problems = append(problems, fmt.Sprintf("%s: %d dump sections, want %d", db.Key(), len(db.Sections), len(Sections)))
			continue
		}
		for i, section := range db.Sections {
			if section.Name != Sections[i] {
				problems = append(problems, fmt.Sprintf("%s: section %d is %q, want %q", db.Key(), i, section.Name, Sections[i]))
			}
		}
	}
	if ch := m.ClickHouse; ch != nil && LedgerDigest(ch.Ledger) != ch.LedgerDigest {
		problems = append(problems, fmt.Sprintf("%s: ledger_digest does not match its ledger rows", ch.Key()))
	}
	if len(problems) > 0 {
		return fmt.Errorf("backup manifest is invalid: %s", strings.Join(problems, "; "))
	}
	return nil
}

// Files returns every file the manifest names for the given keys; an empty key set selects every file.
func (m *Manifest) Files(keys map[string]bool) []File {
	var files []File
	for _, db := range m.Databases {
		if len(keys) > 0 && !keys[db.Key()] {
			continue
		}
		for _, section := range db.Sections {
			files = append(files, section.File)
		}
	}
	if ch := m.ClickHouse; ch != nil && (len(keys) == 0 || keys[ch.Key()]) {
		for _, table := range ch.Tables {
			files = append(files, table.File)
		}
	}
	return files
}

// WriteManifest stores the manifest at the backup location. It is the last object of a backup.
func WriteManifest(ctx context.Context, loc Location, m *Manifest) error {
	if err := m.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode backup manifest: %w", err)
	}
	w, err := loc.Create(ctx, ManifestName)
	if err != nil {
		return err
	}
	if _, err := w.Write(append(data, '\n')); err != nil {
		w.Abort()
		return fmt.Errorf("write backup manifest: %w", err)
	}
	return w.Commit()
}

// ReadManifest loads and validates the manifest of the backup at loc. A missing manifest means the backup never
// completed.
func ReadManifest(ctx context.Context, loc Location) (*Manifest, error) {
	r, err := loc.Open(ctx, ManifestName)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("%s has no %s; the backup is incomplete or the path is wrong", loc, ManifestName)
		}
		return nil, err
	}
	defer r.Close()
	data, err := io.ReadAll(io.LimitReader(r, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("read backup manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("decode backup manifest at %s: %w", loc, err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// VerifyFiles re-reads every named file and compares its size and SHA-256 with the manifest.
func VerifyFiles(ctx context.Context, loc Location, files []File) error {
	var problems []string
	for _, f := range files {
		if err := verifyFile(ctx, loc, f); err != nil {
			problems = append(problems, err.Error())
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("backup files at %s do not verify: %s", loc, strings.Join(problems, "; "))
	}
	return nil
}

// VerifyingReader passes an object through while hashing it; Close reports a size or SHA-256 that differs from
// the manifest, so a restore that streamed a damaged file fails even though the load itself succeeded.
type VerifyingReader struct {
	r    io.ReadCloser
	want File
	h    hash.Hash
	n    int64
}

// NewVerifyingReader wraps r, which must hold the object want describes.
func NewVerifyingReader(r io.ReadCloser, want File) *VerifyingReader {
	return &VerifyingReader{r: r, want: want, h: sha256.New()}
}

func (v *VerifyingReader) Read(p []byte) (int, error) {
	n, err := v.r.Read(p)
	v.h.Write(p[:n])
	v.n += int64(n)
	return n, err
}

// Close closes the object and compares what passed through with the manifest.
func (v *VerifyingReader) Close() error {
	closeErr := v.r.Close()
	if v.n != v.want.Size {
		return fmt.Errorf("%s: streamed %d bytes, manifest says %d", v.want.Path, v.n, v.want.Size)
	}
	if sum := hex.EncodeToString(v.h.Sum(nil)); sum != v.want.SHA256 {
		return fmt.Errorf("%s: sha256 %s, manifest says %s", v.want.Path, sum, v.want.SHA256)
	}
	return closeErr
}

func verifyFile(ctx context.Context, loc Location, f File) error {
	r, err := loc.Open(ctx, f.Path)
	if err != nil {
		return fmt.Errorf("%s: %w", f.Path, err)
	}
	defer r.Close()
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return fmt.Errorf("%s: read: %w", f.Path, err)
	}
	if n != f.Size {
		return fmt.Errorf("%s: size %d, manifest says %d", f.Path, n, f.Size)
	}
	if sum := hex.EncodeToString(h.Sum(nil)); sum != f.SHA256 {
		return fmt.Errorf("%s: sha256 %s, manifest says %s", f.Path, sum, f.SHA256)
	}
	return nil
}
