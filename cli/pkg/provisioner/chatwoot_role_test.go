package provisioner

import (
	"context"
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"
)

func TestChatwootEnvMapUsesNamedPostgresAndRedis(t *testing.T) {
	env := chatwootEnvMap(ServiceConfig{
		EnvVars: map[string]string{
			"DATABASE_HOST":              "yuga-eu-1.internal",
			"DATABASE_PORT":              "5433",
			"DATABASE_USER":              "postgres",
			"DATABASE_PASSWORD":          "pgsecret",
			"POSTGRES_CHATWOOT_HOST":     "127.0.0.1",
			"POSTGRES_CHATWOOT_PORT":     "5432",
			"POSTGRES_CHATWOOT_PASSWORD": "chatwoot-secret",
			"REDIS_CHATWOOT_ADDR":        "127.0.0.1:6380",
			"REDIS_CHATWOOT_PASSWORD":    "redis secret",
			"FROM_EMAIL":                 "support@frameworks.network",
		},
	})

	assertEnv := func(key string, want any) {
		t.Helper()
		if got := env[key]; got != want {
			t.Fatalf("%s = %v, want %v", key, got, want)
		}
	}

	assertEnv("POSTGRES_HOST", "host.docker.internal")
	assertEnv("POSTGRES_PORT", "5432")
	assertEnv("POSTGRES_DATABASE", "chatwoot")
	assertEnv("POSTGRES_USERNAME", "chatwoot")
	assertEnv("POSTGRES_PASSWORD", "chatwoot-secret")
	assertEnv("REDIS_URL", "redis://:redis+secret@host.docker.internal:6380")
	assertEnv("MAILER_SENDER_EMAIL", "support@frameworks.network")
}

func TestChatwootRoleVarsResolvesPinnedImageFromReleaseManifest(t *testing.T) {
	repo := writeTestGitopsRelease(t, `
platform_version: vtest
infrastructure:
  - name: chatwoot
    image: chatwoot/chatwoot:v4.13.0
    digest: sha256:chatwootdigest
`)

	vars, err := chatwootRoleVars(context.Background(), inventory.Host{}, ServiceConfig{
		Version:  "stable",
		Metadata: map[string]any{"gitops_repository": repo},
	}, RoleBuildHelpers{})
	if err != nil {
		t.Fatalf("chatwootRoleVars: %v", err)
	}
	if got := vars["chatwoot_image"]; got != "chatwoot/chatwoot:v4.13.0@sha256:chatwootdigest" {
		t.Fatalf("chatwoot_image = %v", got)
	}
}

func TestChatwootComposePrecreatesSettingsColumnBeforeV4Migration(t *testing.T) {
	content := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/chatwoot/templates/compose.yml.j2")
	for _, want := range []string{
		`ALTER TABLE public.accounts ADD COLUMN IF NOT EXISTS settings jsonb DEFAULT ''{}''::jsonb`,
		`INSERT INTO public.schema_migrations (version) VALUES (''20250421082927'') ON CONFLICT DO NOTHING`,
		`to_regclass('public.accounts') IS NOT NULL`,
		`to_regclass('public.schema_migrations') IS NOT NULL`,
		`information_schema.columns`,
		"bundle exec rails db:chatwoot_prepare",
		`\gexec`,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("chatwoot compose should precreate settings before v4 migration; missing %q:\n%s", want, content)
		}
	}
	if strings.Index(content, "ADD COLUMN IF NOT EXISTS settings") > strings.Index(content, "bundle exec rails db:chatwoot_prepare") {
		t.Fatalf("chatwoot settings preflight must run before db:chatwoot_prepare:\n%s", content)
	}
	if strings.Index(content, "20250421082927") > strings.Index(content, "bundle exec rails db:chatwoot_prepare") {
		t.Fatalf("chatwoot settings migration must be recorded before db:chatwoot_prepare:\n%s", content)
	}
}
