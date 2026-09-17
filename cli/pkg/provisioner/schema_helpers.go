package provisioner

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
)

type SchemaDatabase struct {
	Name        string
	Owner       string
	RuntimeRole string
	SourceName  string
	Schema      string
	// ReapplyBaseline makes the schema role apply the baseline even when the
	// service schema already has tables. Baselines are idempotent, so a second
	// apply only creates the objects that are missing.
	ReapplyBaseline bool
}

// ValidateRuntimeRoleMetadata rejects unusable runtime-role credentials before
// provisioning starts. Callers should invoke it before remote detection and the
// role builders invoke it again as a defense-in-depth boundary.
func ValidateRuntimeRoleMetadata(databases []map[string]string, defaultOwnerPassword, defaultRuntimePassword string) error {
	for _, database := range databases {
		name := strings.TrimSpace(database["name"])
		owner := strings.TrimSpace(database["owner"])
		if owner == "" {
			owner = name
		}
		runtimeRole := strings.TrimSpace(database["runtime_role"])
		if runtimeRole != "" && runtimeRole == owner {
			return fmt.Errorf("database %q: runtime role %q must differ from owner", name, runtimeRole)
		}
		ownerPassword := strings.TrimSpace(database["password"])
		if ownerPassword == "" {
			ownerPassword = strings.TrimSpace(defaultOwnerPassword)
		}
		runtimePassword := strings.TrimSpace(database["runtime_password"])
		if runtimePassword == "" {
			runtimePassword = strings.TrimSpace(defaultRuntimePassword)
		}
		if runtimeRole != "" && runtimePassword == "" {
			return fmt.Errorf("database %q: runtime role %q has no runtime password", name, runtimeRole)
		}
		if ownerPassword != "" && runtimePassword != "" && ownerPassword == runtimePassword {
			return fmt.Errorf("database %q: runtime password must differ from owner password", name)
		}
	}
	return nil
}

// ServiceDatabasesWithBaseline returns the configured databases whose embedded
// baseline carries executable DDL, with owner, runtime role, baseline source,
// and schema defaults applied, one entry per physical database, sorted by name.
// These are the service-owned databases provisioning and the release database
// bootstrap create from a baseline; databases without a baseline (for example
// third-party application databases) are not included.
func ServiceDatabasesWithBaseline(databases []SchemaDatabase) ([]SchemaDatabase, error) {
	unique := make(map[string]SchemaDatabase, len(databases))
	for _, database := range databases {
		if normalized, ok := normalizeSchemaDatabase(database); ok {
			unique[normalized.Name] = normalized
		}
	}
	names := make([]string, 0, len(unique))
	for db := range unique {
		names = append(names, db)
	}
	sort.Strings(names)

	out := make([]SchemaDatabase, 0, len(names))
	for _, db := range names {
		database := unique[db]
		if _, ok, err := embeddedBaselineSQL(database.SourceName); err != nil {
			return nil, err
		} else if ok {
			out = append(out, database)
		}
	}
	return out, nil
}

// normalizeSchemaDatabase applies the defaults the postgres and yugabyte schema
// roles use: owner is the database name, runtime role is <owner>_runtime, the
// baseline source is the database name, and the schema is the source name.
func normalizeSchemaDatabase(database SchemaDatabase) (SchemaDatabase, bool) {
	db := strings.TrimSpace(database.Name)
	if db == "" {
		return SchemaDatabase{}, false
	}
	owner := strings.TrimSpace(database.Owner)
	if owner == "" {
		owner = db
	}
	source := strings.TrimSpace(database.SourceName)
	if source == "" {
		source = db
	}
	schema := strings.TrimSpace(database.Schema)
	if schema == "" {
		schema = source
	}
	runtimeRole := strings.TrimSpace(database.RuntimeRole)
	if runtimeRole == "" {
		runtimeRole = owner + "_runtime"
	}
	return SchemaDatabase{Name: db, Owner: owner, RuntimeRole: runtimeRole, SourceName: source, Schema: schema, ReapplyBaseline: database.ReapplyBaseline}, true
}

// embeddedBaselineSQL returns the embedded baseline for a source database and
// whether it exists with executable DDL.
func embeddedBaselineSQL(source string) (string, bool, error) {
	schemaPath := path.Join("schema", source+".sql")
	data, err := dbsql.Content.ReadFile(schemaPath)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read %s: %w", schemaPath, err)
	}
	schemaSQL := string(data)
	if strings.TrimSpace(schemaSQL) == "" || !hasExecutableSchemaDDL(schemaSQL) {
		return "", false, nil
	}
	return schemaSQL, true, nil
}

// BuildSchemaItems materializes embedded baseline schemas matching configured
// database names to local temp files. Returns {db, schema, owner, src} entries
// suitable for postgres_schema_items / yugabyte_schema_items role vars; Ansible
// copies the file bytes, executes them with community.postgresql, and grants
// ownership to the application role.
func BuildSchemaItems(databases []SchemaDatabase) ([]map[string]any, func(), error) {
	if len(databases) == 0 {
		return nil, func() {}, nil
	}
	withBaseline, err := ServiceDatabasesWithBaseline(databases)
	if err != nil {
		return nil, func() {}, err
	}

	items := make([]map[string]any, 0, len(withBaseline))
	var cleanupPaths []string
	cleanup := func() {
		for _, p := range cleanupPaths {
			_ = os.Remove(p)
		}
	}
	for _, database := range withBaseline {
		db := database.Name
		schema := database.Schema
		schemaSQL, _, err := embeddedBaselineSQL(database.SourceName)
		if err != nil {
			cleanup()
			return nil, func() {}, err
		}
		file, err := os.CreateTemp("", fmt.Sprintf("frameworks-schema-%s-*.sql", safeSchemaFilePrefix(db)))
		if err != nil {
			cleanup()
			return nil, func() {}, fmt.Errorf("create temp schema file for %s: %w", db, err)
		}
		localPath := file.Name()
		if _, err := file.WriteString(schemaSQL); err != nil {
			file.Close()
			cleanup()
			_ = os.Remove(localPath)
			return nil, func() {}, fmt.Errorf("write temp schema file for %s: %w", db, err)
		}
		if err := file.Close(); err != nil {
			cleanup()
			_ = os.Remove(localPath)
			return nil, func() {}, fmt.Errorf("close temp schema file for %s: %w", db, err)
		}
		cleanupPaths = append(cleanupPaths, localPath)
		items = append(items, map[string]any{
			"db":           db,
			"schema":       schema,
			"owner":        database.Owner,
			"runtime_role": database.RuntimeRole,
			"src":          filepath.ToSlash(localPath),
			"reapply":      database.ReapplyBaseline,
		})
	}
	return items, cleanup, nil
}

func BuildSchemaItemsForNames(dbNames []string) ([]map[string]any, func(), error) {
	databases := make([]SchemaDatabase, 0, len(dbNames))
	for _, db := range dbNames {
		databases = append(databases, SchemaDatabase{Name: db, Owner: db})
	}
	return BuildSchemaItems(databases)
}

func hasExecutableSchemaDDL(sql string) bool {
	cleaned := stripSQLLineComments(sql)
	upper := strings.ToUpper(cleaned)
	return strings.Contains(upper, "CREATE SCHEMA") ||
		strings.Contains(upper, "CREATE TABLE") ||
		strings.Contains(upper, "ALTER TABLE")
}

func stripSQLLineComments(sql string) string {
	var out strings.Builder
	for _, line := range strings.Split(sql, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") || trimmed == "" {
			continue
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return out.String()
}

func safeSchemaFilePrefix(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "database"
	}
	return b.String()
}
