package database_test

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

var postgresRowSystemColumn = regexp.MustCompile(`(?i)\b(ctid|xmin|xmax|tableoid)\b`)

func TestRuntimeSQLAvoidsPostgresRowSystemColumns(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve source path")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	matches, err := filepath.Glob(filepath.Join(repoRoot, "api_*", "internal", "database", "queries"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("no canonical runtime SQL query directories found")
	}
	for _, dir := range matches {
		err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || filepath.Ext(path) != ".sql" {
				return nil
			}
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			defer file.Close()
			scanner := bufio.NewScanner(file)
			line := 0
			for scanner.Scan() {
				line++
				text := scanner.Text()
				if postgresRowSystemColumn.MatchString(text) {
					rel, _ := filepath.Rel(repoRoot, path)
					t.Errorf("%s:%d uses PostgreSQL row-system column in runtime SQL: %s", rel, line, strings.TrimSpace(text))
				}
			}
			return scanner.Err()
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
}
