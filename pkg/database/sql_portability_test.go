package database_test

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
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
		sqlWalkErr := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || filepath.Ext(path) != ".sql" {
				return nil
			}
			return scanRuntimeSourceForRowSystemColumns(t, repoRoot, path)
		})
		if sqlWalkErr != nil {
			t.Fatalf("walk %s: %v", dir, sqlWalkErr)
		}
	}

	goRoots, err := filepath.Glob(filepath.Join(repoRoot, "api_*"))
	if err != nil {
		t.Fatal(err)
	}
	goRoots = append(goRoots, filepath.Join(repoRoot, "pkg"))
	for _, dir := range goRoots {
		goWalkErr := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			return scanGoSourceForRowSystemColumns(t, repoRoot, path)
		})
		if goWalkErr != nil {
			t.Fatalf("walk %s: %v", dir, goWalkErr)
		}
	}
}

func scanGoSourceForRowSystemColumns(t *testing.T, repoRoot, path string) error {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return err
	}
	ast.Inspect(file, func(node ast.Node) bool {
		expr, ok := node.(ast.Expr)
		if !ok {
			return true
		}
		value, ok := staticGoString(expr)
		if !ok {
			return true
		}
		// A fully static expression has already incorporated its child literals.
		// Do not inspect those fragments independently: a log prefix concatenated
		// with the word "xmax" is still not a SQL statement.
		if !isSQLBearingGoString(value) || !postgresRowSystemColumn.MatchString(value) {
			return false
		}
		rel, _ := filepath.Rel(repoRoot, path)
		position := fset.Position(expr.Pos())
		t.Errorf("%s:%d uses PostgreSQL row-system column in runtime string: %s", rel, position.Line, strings.TrimSpace(value))
		return false
	})
	return nil
}

func staticGoString(expr ast.Expr) (string, bool) {
	switch value := expr.(type) {
	case *ast.BasicLit:
		if value.Kind != token.STRING {
			return "", false
		}
		unquoted, err := strconv.Unquote(value.Value)
		return unquoted, err == nil
	case *ast.BinaryExpr:
		if value.Op != token.ADD {
			return "", false
		}
		left, leftOK := staticGoString(value.X)
		right, rightOK := staticGoString(value.Y)
		return left + right, leftOK && rightOK
	case *ast.ParenExpr:
		return staticGoString(value.X)
	default:
		return "", false
	}
}

func isSQLBearingGoString(value string) bool {
	remaining := strings.TrimSpace(value)
	for strings.HasPrefix(remaining, "--") {
		if newline := strings.IndexByte(remaining, '\n'); newline >= 0 {
			remaining = strings.TrimSpace(remaining[newline+1:])
			continue
		}
		return false
	}
	if remaining == "" {
		return false
	}
	fields := strings.Fields(remaining)
	if len(fields) == 0 {
		return false
	}
	first := strings.TrimSuffix(strings.ToUpper(fields[0]), ";")
	switch first {
	case "SELECT", "WITH", "INSERT", "UPDATE", "DELETE", "MERGE", "CREATE", "ALTER", "DROP", "TRUNCATE", "GRANT", "REVOKE", "DO", "CALL", "COPY", "SET", "SHOW", "EXPLAIN":
		return true
	default:
		return false
	}
}

func TestSQLBearingGoStringClassification(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "select", value: "SELECT xmax FROM jobs", want: true},
		{name: "select newline", value: "SELECT\nxmax FROM jobs", want: true},
		{name: "sqlc comment", value: "-- name: ClaimJob :one\nWITH candidate AS (SELECT xmax FROM jobs)", want: true},
		{name: "log text", value: "Yugabyte rejected incompatible xmax usage", want: false},
		{name: "error text", value: "ctid is not portable", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSQLBearingGoString(tt.value); got != tt.want {
				t.Fatalf("isSQLBearingGoString(%q) = %t, want %t", tt.value, got, tt.want)
			}
		})
	}
}

func TestStaticGoString(t *testing.T) {
	expr, err := parser.ParseExpr("`SELECT ` + `xmax FROM jobs`")
	if err != nil {
		t.Fatal(err)
	}
	value, ok := staticGoString(expr)
	if !ok || value != "SELECT xmax FROM jobs" {
		t.Fatalf("staticGoString() = (%q, %t), want concatenated SQL", value, ok)
	}
}

func scanRuntimeSourceForRowSystemColumns(t *testing.T, repoRoot, path string) error {
	t.Helper()
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
			t.Errorf("%s:%d uses PostgreSQL row-system column in runtime source: %s", rel, line, strings.TrimSpace(text))
		}
	}
	return scanner.Err()
}
