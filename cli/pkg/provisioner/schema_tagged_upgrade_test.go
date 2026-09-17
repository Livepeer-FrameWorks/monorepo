//go:build schema_verify

package provisioner

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strings"
	"testing"

	"frameworks/cli/internal/releases"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
)

const schemaVerifyFromTagEnv = "FRAMEWORKS_SCHEMA_VERIFY_FROM_TAG"

func schemaVerifyTagSetting() (tag string, skip bool, err error) {
	tag = strings.TrimSpace(os.Getenv(schemaVerifyFromTagEnv))
	if tag != "" {
		return tag, false, nil
	}
	if strings.TrimSpace(os.Getenv("CI")) != "" {
		return "", false, fmt.Errorf("%s is unset in CI; tagged-baseline upgrade proof must not skip", schemaVerifyFromTagEnv)
	}
	return "", true, nil
}

func schemaVerifyFromTag(t *testing.T) string {
	t.Helper()
	tag, skip, settingErr := schemaVerifyTagSetting()
	if settingErr != nil {
		t.Fatal(settingErr)
	}
	if skip {
		t.Skipf("%s is unset; tagged-baseline upgrade proof is opt-in", schemaVerifyFromTagEnv)
	}
	if err := releases.ValidateVersion(tag); err != nil || releases.BaseVersion(tag) != tag {
		t.Fatalf("%s must be a final vX.Y.Z tag, got %q", schemaVerifyFromTagEnv, tag)
	}
	cmd := exec.Command("git", "merge-base", "--is-ancestor", tag, "HEAD")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s=%s is not a reachable shipped tag: %v\n%s", schemaVerifyFromTagEnv, tag, err, out)
	}
	return tag
}

func TestSchemaVerifyFromTagIsRequiredInCI(t *testing.T) {
	t.Setenv(schemaVerifyFromTagEnv, "")
	t.Setenv("CI", "true")
	if _, skip, err := schemaVerifyTagSetting(); err == nil || skip {
		t.Fatalf("CI tag setting = skip:%v err:%v, want hard failure", skip, err)
	}
}

func repositoryFileAtTag(t *testing.T, tag, file string) string {
	t.Helper()
	cmd := exec.Command("git", "show", tag+":"+file)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("read %s at %s: %v\n%s", file, tag, err, out)
	}
	return string(out)
}

// baselineAtTagOrRelease returns the baseline an upgraded cluster starts from
// for one embedded schema file (relative to pkg/database/sql). A database that
// existed at the tag starts from its tagged baseline. A database introduced
// after the tag does not exist on an upgraded cluster; the release creates it
// from its current baseline, so that baseline is returned, and a post-tag
// migration for such a database fails the proof because nothing could apply it.
func baselineAtTagOrRelease(t *testing.T, tag, file string, postTag []Migration) string {
	t.Helper()
	repositoryPath := "pkg/database/sql/" + file
	err := exec.Command("git", "cat-file", "-e", tag+":"+repositoryPath).Run()
	if err == nil {
		return repositoryFileAtTag(t, tag, repositoryPath)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("probe %s at %s: %v", repositoryPath, tag, err)
	}
	database := strings.TrimSuffix(path.Base(file), ".sql")
	for _, migration := range postTag {
		if migration.Database == database {
			t.Fatalf("database %s has no baseline at %s but migration %s/%s/%s targets it; a database new in this release is created from its current baseline and must not ship migrations", database, tag, migration.Version, migration.Phase, migration.Filename)
		}
	}
	current, readErr := dbsql.Content.ReadFile(file)
	if readErr != nil {
		t.Fatalf("read current %s: %v", file, readErr)
	}
	t.Logf("%s is new after %s; the release creates it from the current baseline", database, tag)
	return string(current)
}

func migrationsAfterVersion(migrations []Migration, version string) []Migration {
	out := make([]Migration, 0, len(migrations))
	for _, migration := range migrations {
		if compareSemver(migration.Version, version) > 0 {
			out = append(out, migration)
		}
	}
	return out
}
