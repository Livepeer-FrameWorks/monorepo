package provisioner

import "testing"

func TestListmonkEnvMapWiresAdminCredsFromGitOps(t *testing.T) {
	env := listmonkEnvMap(ServiceConfig{
		EnvVars: map[string]string{
			"DATABASE_HOST":     "pg.frameworks",
			"DATABASE_PORT":     "5432",
			"DATABASE_USER":     "postgres",
			"DATABASE_PASSWORD": "pgsecret",
			"LISTMONK_USERNAME": "admin",
			"LISTMONK_PASSWORD": "from-sops",
		},
	})

	if got := env["LISTMONK_ADMIN_USER"]; got != "admin" {
		t.Fatalf("LISTMONK_ADMIN_USER = %v, want %q (sourced from LISTMONK_USERNAME)", got, "admin")
	}
	if got := env["LISTMONK_ADMIN_PASSWORD"]; got != "from-sops" {
		t.Fatalf("LISTMONK_ADMIN_PASSWORD = %v, want %q (sourced from LISTMONK_PASSWORD)", got, "from-sops")
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
