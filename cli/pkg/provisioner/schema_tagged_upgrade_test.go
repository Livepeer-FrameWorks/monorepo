//go:build schema_verify

package provisioner

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"frameworks/cli/internal/releases"
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

func migrationsAfterVersion(migrations []Migration, version string) []Migration {
	out := make([]Migration, 0, len(migrations))
	for _, migration := range migrations {
		if compareSemver(migration.Version, version) > 0 {
			out = append(out, migration)
		}
	}
	return out
}
