package provisioner

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"

	dbsql "frameworks/pkg/database/sql"
)

// Migration represents a single versioned SQL migration file. Consumed by
// BuildMigrationItems, which hands the set to the postgres / yugabyte role
// via *_migrate_items vars; the role's tasks/migrate.yml does the apply.
type Migration struct {
	Version  string // e.g. "v1.1.0"
	Sequence int    // parsed from NNN prefix
	Filename string // e.g. "001_purser_add_invoice_field.sql"
	Path     string // full embed path
	Checksum string // SHA-256 of content
	content  string
}

// discoverMigrations walks the embedded FS under root looking for
// versioned migration directories (e.g. migrations/v1.0.0/001_foo.sql).
func discoverMigrations(root string) ([]Migration, error) {
	var out []Migration
	err := fs.WalkDir(dbsql.Content, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".sql") {
			return err
		}
		// Expected: root/vX.Y.Z/NNN_description.sql
		dir := path.Dir(p)
		ver := path.Base(dir)
		if !strings.HasPrefix(ver, "v") {
			return nil
		}
		base := path.Base(p)
		seq := parseSequence(base)

		data, readErr := dbsql.Content.ReadFile(p)
		if readErr != nil {
			return readErr
		}
		checksum := fmt.Sprintf("%x", sha256.Sum256(data))

		out = append(out, Migration{
			Version:  ver,
			Sequence: seq,
			Filename: base,
			Path:     p,
			Checksum: checksum,
			content:  string(data),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Version != out[j].Version {
			return compareSemver(out[i].Version, out[j].Version) < 0
		}
		return out[i].Sequence < out[j].Sequence
	})
	return out, nil
}

func parseSequence(filename string) int {
	// NNN_description.sql -> NNN
	idx := strings.Index(filename, "_")
	if idx <= 0 {
		return 0
	}
	n, _ := strconv.Atoi(filename[:idx]) //nolint:errcheck // best-effort parse, returns 0 on failure
	return n
}

// compareSemver compares two version strings like "v1.2.3".
// Returns -1 if a < b, 0 if equal, 1 if a > b.
// Falls back to lexicographic comparison on parse failure.
func compareSemver(a, b string) int {
	parseVer := func(s string) [3]int {
		s = strings.TrimPrefix(s, "v")
		parts := strings.SplitN(s, ".", 3)
		var v [3]int
		for i := 0; i < len(parts) && i < 3; i++ {
			v[i], _ = strconv.Atoi(parts[i]) //nolint:errcheck // best-effort parse, returns 0 on failure
		}
		return v
	}
	va, vb := parseVer(a), parseVer(b)
	for i := range 3 {
		if va[i] < vb[i] {
			return -1
		}
		if va[i] > vb[i] {
			return 1
		}
	}
	return 0
}
