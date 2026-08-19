//go:build schema_verify

package provisioner

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"frameworks/cli/internal/releases"
)

const schemaVerifyFromTagEnv = "FRAMEWORKS_SCHEMA_VERIFY_FROM_TAG"

func schemaVerifyFromTag(t *testing.T) string {
	t.Helper()
	tag := strings.TrimSpace(os.Getenv(schemaVerifyFromTagEnv))
	if tag == "" {
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
