package provisioner

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
)

// BuildClickHouseSchemaItems materializes the embedded baseline ClickHouse
// schema for each configured database. Returns {name, database, sql} entries
// suitable for the clickhouse_schema_items role var consumed by
// roles/clickhouse/tasks/schema.yml.
//
// Mirrors BuildSchemaItems for Postgres but uses the seed-style {name,
// database, sql} shape because the ClickHouse role stages SQL via
// `copy: content:` rather than copying a local file. Empty/whitespace-only
// schemas are skipped.
func BuildClickHouseSchemaItems(dbNames []string) ([]map[string]any, error) {
	if len(dbNames) == 0 {
		return nil, nil
	}
	unique := make(map[string]struct{}, len(dbNames))
	for _, name := range dbNames {
		if name = strings.TrimSpace(name); name != "" {
			unique[name] = struct{}{}
		}
	}
	names := make([]string, 0, len(unique))
	for name := range unique {
		names = append(names, name)
	}
	sort.Strings(names)

	items := make([]map[string]any, 0, len(names))
	for _, name := range names {
		schemaPath := path.Join("clickhouse", name+".sql")
		data, err := dbsql.Content.ReadFile(schemaPath)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", schemaPath, err)
		}
		if strings.TrimSpace(string(data)) == "" {
			continue
		}
		items = append(items, map[string]any{
			"name":     name,
			"database": name,
			"sql":      string(data),
		})
	}
	return items, nil
}
