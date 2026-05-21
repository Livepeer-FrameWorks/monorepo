package provisioner

import (
	"context"
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"
)

func TestListmonkEnvMapWiresAdminCredsFromGitOps(t *testing.T) {
	env := listmonkEnvMap(ServiceConfig{
		EnvVars: map[string]string{
			"DATABASE_HOST":             "yuga-eu-1.internal",
			"DATABASE_PORT":             "5433",
			"DATABASE_USER":             "postgres",
			"DATABASE_PASSWORD":         "pgsecret",
			"POSTGRES_SUPPORT_HOST":     "127.0.0.1",
			"POSTGRES_SUPPORT_PORT":     "5432",
			"POSTGRES_SUPPORT_PASSWORD": "support-secret",
			"LISTMONK_ADMIN_USER":       "admin",
			"LISTMONK_ADMIN_PASSWORD":   "from-sops",
			"LISTMONK_FRONTEND_URL":     "https://listmonk.frameworks.network",
		},
	})

	if got := env["LISTMONK_ADMIN_USER"]; got != "admin" {
		t.Fatalf("LISTMONK_ADMIN_USER = %v, want %q", got, "admin")
	}
	if got := env["LISTMONK_ADMIN_PASSWORD"]; got != "from-sops" {
		t.Fatalf("LISTMONK_ADMIN_PASSWORD = %v, want %q", got, "from-sops")
	}
	if got := env["LISTMONK_db__host"]; got != "host.docker.internal" {
		t.Fatalf("LISTMONK_db__host = %v, want host.docker.internal", got)
	}
	if got := env["LISTMONK_db__port"]; got != "5432" {
		t.Fatalf("LISTMONK_db__port = %v, want 5432", got)
	}
	if got := env["LISTMONK_db__password"]; got != "support-secret" {
		t.Fatalf("LISTMONK_db__password = %v, want support-secret", got)
	}
	if got := env["LISTMONK_app__root"]; got != "https://listmonk.frameworks.network" {
		t.Fatalf("LISTMONK_app__root = %v, want public URL", got)
	}
}

func TestListmonkRoleVarsWiresPublicURLReconcileDatabase(t *testing.T) {
	vars, err := listmonkRoleVars(context.Background(), inventory.Host{}, ServiceConfig{
		Image: "listmonk/listmonk:v6.1.0@sha256:test",
		EnvVars: map[string]string{
			"DATABASE_HOST":             "yuga-eu-1.internal",
			"DATABASE_PORT":             "5433",
			"DATABASE_USER":             "postgres",
			"DATABASE_PASSWORD":         "pgsecret",
			"POSTGRES_SUPPORT_HOST":     "127.0.0.1",
			"POSTGRES_SUPPORT_PORT":     "5432",
			"POSTGRES_SUPPORT_PASSWORD": "support-secret",
			"LISTMONK_FRONTEND_URL":     "https://listmonk.frameworks.network",
		},
	}, RoleBuildHelpers{})
	if err != nil {
		t.Fatalf("listmonkRoleVars returned error: %v", err)
	}

	if got := vars["listmonk_public_url"]; got != "https://listmonk.frameworks.network" {
		t.Fatalf("listmonk_public_url = %v, want public URL", got)
	}
	if got := vars["listmonk_db_host"]; got != "127.0.0.1" {
		t.Fatalf("listmonk_db_host = %v, want Postgres host from host perspective", got)
	}
	if got := vars["listmonk_db_port"]; got != "5432" {
		t.Fatalf("listmonk_db_port = %v, want support Postgres port", got)
	}
	if got := vars["listmonk_db_password"]; got != "support-secret" {
		t.Fatalf("listmonk_db_password = %v, want support Postgres password", got)
	}
}

func TestListmonkRoleVarsResolvesPinnedImageFromReleaseManifest(t *testing.T) {
	repo := writeTestGitopsRelease(t, `
platform_version: vtest
infrastructure:
  - name: listmonk
    image: listmonk/listmonk:v6.1.0
    digest: sha256:listmonxdigest
`)

	vars, err := listmonkRoleVars(context.Background(), inventory.Host{}, ServiceConfig{
		Version:  "stable",
		Metadata: map[string]any{"gitops_repository": repo},
	}, RoleBuildHelpers{})
	if err != nil {
		t.Fatalf("listmonkRoleVars: %v", err)
	}
	if got := vars["listmonk_image"]; got != "listmonk/listmonk:v6.1.0@sha256:listmonxdigest" {
		t.Fatalf("listmonk_image = %v", got)
	}
}

func TestListmonkEnvMapDoesNotSubstituteAdminCredsWhenMissing(t *testing.T) {
	// Preflight in pkg/servicedefs is the gate that fails closed when these
	// are empty. The role itself must not invent fallbacks ("admin"/"admin"),
	// because that would silently bypass the gate if preflight is skipped
	// (--ignore-validation) or if a future caller stops checking.
	env := listmonkEnvMap(ServiceConfig{
		EnvVars: map[string]string{
			"DATABASE_HOST": "pg.frameworks",
		},
	})

	if got := env["LISTMONK_ADMIN_USER"]; got != "" {
		t.Fatalf("LISTMONK_ADMIN_USER = %v, want empty (no silent fallback)", got)
	}
	if got := env["LISTMONK_ADMIN_PASSWORD"]; got != "" {
		t.Fatalf("LISTMONK_ADMIN_PASSWORD = %v, want empty (no silent fallback)", got)
	}
}

func TestListmonkComposeRunsDatabaseUpgradeBeforeServing(t *testing.T) {
	content := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/listmonk/templates/compose.yml.j2")
	for _, want := range []string{
		"./listmonk --install --idempotent --yes",
		"./listmonk --upgrade --yes",
		"&& ./listmonk",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("listmonk compose should install, upgrade, then serve; missing %q:\n%s", want, content)
		}
	}
	if strings.Index(content, "--upgrade --yes") < strings.Index(content, "--install --idempotent --yes") {
		t.Fatalf("listmonk upgrade must run after idempotent install:\n%s", content)
	}
	if strings.LastIndex(content, "&& ./listmonk") < strings.Index(content, "--upgrade --yes") {
		t.Fatalf("listmonk server must start after database upgrade:\n%s", content)
	}
}
