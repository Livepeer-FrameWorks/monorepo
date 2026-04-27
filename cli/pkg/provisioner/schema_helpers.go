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

	dbsql "frameworks/pkg/database/sql"
)

type SchemaDatabase struct {
	Name  string
	Owner string
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

	unique := make(map[string]SchemaDatabase, len(databases))
	for _, database := range databases {
		db := strings.TrimSpace(database.Name)
		if db != "" {
			owner := strings.TrimSpace(database.Owner)
			if owner == "" {
				owner = db
			}
			unique[db] = SchemaDatabase{Name: db, Owner: owner}
		}
	}
	names := make([]string, 0, len(unique))
	for db := range unique {
		names = append(names, db)
	}
	sort.Strings(names)

	items := make([]map[string]any, 0, len(names))
	var cleanupPaths []string
	cleanup := func() {
		for _, p := range cleanupPaths {
			_ = os.Remove(p)
		}
	}
	for _, db := range names {
		database := unique[db]
		schemaPath := path.Join("schema", db+".sql")
		data, err := dbsql.Content.ReadFile(schemaPath)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			cleanup()
			return nil, func() {}, fmt.Errorf("read %s: %w", schemaPath, err)
		}
		if strings.TrimSpace(string(data)) == "" {
			continue
		}
		schemaSQL := string(data)
		if !hasExecutableSchemaDDL(schemaSQL) {
			continue
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
			"db":     db,
			"schema": db,
			"owner":  database.Owner,
			"src":    filepath.ToSlash(localPath),
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
