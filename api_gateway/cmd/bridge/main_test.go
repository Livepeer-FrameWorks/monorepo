package main

import (
	"os"
	"path/filepath"
	"testing"

	"frameworks/pkg/logging"

	"github.com/vektah/gqlparser/v2/ast"
)

func TestIsIntrospectionOperationAllFields(t *testing.T) {
	op := &ast.OperationDefinition{
		SelectionSet: ast.SelectionSet{
			&ast.Field{Name: "__schema"},
			&ast.Field{Name: "__type"},
		},
	}

	if !isIntrospectionOperation(op) {
		t.Fatal("expected introspection operation to be recognized")
	}
}

func TestIsIntrospectionOperationMixedFields(t *testing.T) {
	op := &ast.OperationDefinition{
		SelectionSet: ast.SelectionSet{
			&ast.Field{Name: "__schema"},
			&ast.Field{Name: "streamsConnection"},
		},
	}

	if isIntrospectionOperation(op) {
		t.Fatal("expected mixed fields to disable introspection bypass")
	}
}

func TestIsIntrospectionOperationInlineFragment(t *testing.T) {
	op := &ast.OperationDefinition{
		SelectionSet: ast.SelectionSet{
			&ast.InlineFragment{
				SelectionSet: ast.SelectionSet{
					&ast.Field{Name: "__schema"},
				},
			},
		},
	}

	if isIntrospectionOperation(op) {
		t.Fatal("expected inline fragments to disable introspection bypass")
	}
}

func TestLoadSkillFilesFromWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	writeSkillFile(t, dir, "SKILL.md", "skill")
	writeSkillFile(t, dir, "skill.json", `{"name":"frameworks"}`)

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("change working directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldWD); err != nil {
			t.Fatalf("restore working directory: %v", err)
		}
	})
	t.Setenv("SKILL_FILES_DIR", "")

	files := loadSkillFiles(logging.NewLoggerWithService("bridge-test"))
	if string(files.skillMD) != "skill" {
		t.Fatalf("skillMD = %q, want %q", files.skillMD, "skill")
	}
	if string(files.skillJSON) != `{"name":"frameworks"}` {
		t.Fatalf("skillJSON = %q, want %q", files.skillJSON, `{"name":"frameworks"}`)
	}
}

func TestLoadSkillFilesFromRepoRootWhenRunFromModule(t *testing.T) {
	root := t.TempDir()
	moduleDir := filepath.Join(root, "api_gateway")
	skillsDir := filepath.Join(root, "docs", "skills")
	if err := os.MkdirAll(moduleDir, 0o755); err != nil {
		t.Fatalf("create module dir: %v", err)
	}
	writeSkillFile(t, skillsDir, "SKILL.md", "module skill")
	writeSkillFile(t, skillsDir, "skill.json", `{"name":"module"}`)

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(moduleDir); err != nil {
		t.Fatalf("change working directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldWD); err != nil {
			t.Fatalf("restore working directory: %v", err)
		}
	})
	t.Setenv("SKILL_FILES_DIR", "")

	files := loadSkillFiles(logging.NewLoggerWithService("bridge-test"))
	if string(files.skillMD) != "module skill" {
		t.Fatalf("skillMD = %q, want %q", files.skillMD, "module skill")
	}
	if string(files.skillJSON) != `{"name":"module"}` {
		t.Fatalf("skillJSON = %q, want %q", files.skillJSON, `{"name":"module"}`)
	}
}

func writeSkillFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create skill dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write skill file: %v", err)
	}
}
