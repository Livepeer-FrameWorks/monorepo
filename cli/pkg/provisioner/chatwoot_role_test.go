package provisioner

import "testing"

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
	assertEnv("REDIS_URL", "redis://host.docker.internal:6380")
}
