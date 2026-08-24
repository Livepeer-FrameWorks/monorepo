package foghorndb

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestProductionDatabaseCallsUseGeneratedQueries keeps executable SQL at the service-owned
// query boundary. Tests may use drivers directly to exercise real-engine contracts; production
// adapters may not grow new handwritten Query/Exec call sites.
func TestProductionDatabaseCallsUseGeneratedQueries(t *testing.T) {
	serviceRoot := filepath.Clean(filepath.Join("..", "..", ".."))
	var violations []string
	err := filepath.WalkDir(serviceRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path == filepath.Join(serviceRoot, "internal", "database", "foghorndb") {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, "_testsupport.go") {
			return nil
		}
		contents, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		text := string(contents)
		for _, call := range []string{".QueryContext(", ".QueryRowContext(", ".ExecContext("} {
			if strings.Contains(text, call) {
				violations = append(violations, path+": "+call)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan production Go sources: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("production database calls must use foghorndb generated queries:\n%s", strings.Join(violations, "\n"))
	}
}
