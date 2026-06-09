package configgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fullBaseEnv = `POSTGRES_HOST=postgres
POSTGRES_PORT=5432
POSTGRES_DB=frameworks
POSTGRES_SSL_MODE=disable
CLICKHOUSE_HOST=clickhouse
CLICKHOUSE_HTTP_PORT=8123
CLICKHOUSE_NATIVE_PORT=9000
CLICKHOUSE_DB=periscope
KAFKA_HOST=kafka
KAFKA_PORT=9092
COMMODORE_HOST=commodore
COMMODORE_PORT=18001
COMMODORE_GRPC_PORT=19001
QUARTERMASTER_HOST=quartermaster
QUARTERMASTER_PORT=18002
QUARTERMASTER_GRPC_PORT=19002
PURSER_HOST=purser
PURSER_PORT=18003
PURSER_GRPC_PORT=19003
PERISCOPE_QUERY_HOST=periscope-query
PERISCOPE_QUERY_PORT=18004
PERISCOPE_QUERY_GRPC_PORT=19004
PERISCOPE_INGEST_HOST=periscope-ingest
PERISCOPE_INGEST_PORT=18005
SIGNALMAN_HOST=signalman
SIGNALMAN_GRPC_PORT=19005
DECKHAND_HOST=deckhand
DECKHAND_GRPC_PORT=19006
SKIPPER_HOST=skipper
SKIPPER_GRPC_PORT=19007
DECKLOG_HOST=decklog
DECKLOG_PORT=18006
HELMSMAN_HOST=helmsman
HELMSMAN_PORT=18007
FOGHORN_HOST=foghorn
FOGHORN_PORT=18008
FOGHORN_INTERNAL_GRPC_PORT=18019
MISTSERVER_HOST=mistserver
MISTSERVER_PORT=4242
MISTSERVER_HTTP_HOST=mistserver
MISTSERVER_HTTP_PORT=8080
NAVIGATOR_HOST=navigator
NAVIGATOR_GRPC_PORT=18011
GATEWAY_PUBLIC_URL=http://localhost:18090
WEBAPP_PUBLIC_URL=http://localhost:18090/app
MARKETING_PUBLIC_URL=http://localhost:18090/marketing
DOCS_PUBLIC_URL=http://localhost:18090/docs
FORMS_PUBLIC_URL=http://localhost:18032
STREAMING_INGEST_URL=http://localhost:8080
STREAMING_PLAY_URL=http://localhost:18008
STREAMING_EDGE_URL=http://localhost:18090/view
`

const fullSecretsEnv = `POSTGRES_USER=frameworks_user
POSTGRES_PASSWORD=change-me
CLICKHOUSE_USER=frameworks
CLICKHOUSE_PASSWORD=change-me
JWT_SECRET=change-me
SERVICE_TOKEN=change-me
`

func writeEnv(t *testing.T, base, secrets string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	basePath := filepath.Join(dir, "base.env")
	secretsPath := filepath.Join(dir, "secrets.env")
	if err := os.WriteFile(basePath, []byte(base), 0o600); err != nil {
		t.Fatalf("write base: %v", err)
	}
	if err := os.WriteFile(secretsPath, []byte(secrets), 0o600); err != nil {
		t.Fatalf("write secrets: %v", err)
	}
	return basePath, secretsPath
}

func TestDefaultsContextFallback(t *testing.T) {
	t.Run("empty context defaults to dev", func(t *testing.T) {
		basePath, secretsPath := writeEnv(t, fullBaseEnv, fullSecretsEnv)
		env, err := Generate(Options{BaseFile: basePath, SecretsFile: secretsPath})
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if got := env["ENV_CONTEXT"]; got != "dev" {
			t.Fatalf("ENV_CONTEXT = %q, want dev", got)
		}
	})

	t.Run("explicit context is preserved", func(t *testing.T) {
		basePath, secretsPath := writeEnv(t, fullBaseEnv, fullSecretsEnv)
		env, err := Generate(Options{BaseFile: basePath, SecretsFile: secretsPath, Context: "production"})
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if got := env["ENV_CONTEXT"]; got != "production" {
			t.Fatalf("ENV_CONTEXT = %q, want production", got)
		}
	})
}

func TestComputeDerivedPortDefaults(t *testing.T) {
	t.Run("absent webapp/website ports get defaults", func(t *testing.T) {
		basePath, secretsPath := writeEnv(t, fullBaseEnv, fullSecretsEnv)
		env, err := Generate(Options{BaseFile: basePath, SecretsFile: secretsPath, Context: "dev"})
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if got := env["WEBAPP_PORT"]; got != "18030" {
			t.Fatalf("WEBAPP_PORT = %q, want 18030", got)
		}
		if got := env["WEBSITE_PORT"]; got != "18031" {
			t.Fatalf("WEBSITE_PORT = %q, want 18031", got)
		}
	})

	t.Run("present webapp/website ports are preserved", func(t *testing.T) {
		base := fullBaseEnv + "WEBAPP_PORT=29000\nWEBSITE_PORT=29001\n"
		basePath, secretsPath := writeEnv(t, base, fullSecretsEnv)
		env, err := Generate(Options{BaseFile: basePath, SecretsFile: secretsPath, Context: "dev"})
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if got := env["WEBAPP_PORT"]; got != "29000" {
			t.Fatalf("WEBAPP_PORT = %q, want 29000", got)
		}
		if got := env["WEBSITE_PORT"]; got != "29001" {
			t.Fatalf("WEBSITE_PORT = %q, want 29001", got)
		}
	})
}

func TestComputeDerivedFoghornRedis(t *testing.T) {
	t.Run("redis url present propagates to REDIS_URL", func(t *testing.T) {
		base := fullBaseEnv + "FOGHORN_REDIS_URL=redis://cache:6379\n"
		basePath, secretsPath := writeEnv(t, base, fullSecretsEnv)
		env, err := Generate(Options{BaseFile: basePath, SecretsFile: secretsPath, Context: "dev"})
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if got := env["REDIS_URL"]; got != "redis://cache:6379" {
			t.Fatalf("REDIS_URL = %q, want redis://cache:6379", got)
		}
	})

	t.Run("redis url absent leaves REDIS_URL unset", func(t *testing.T) {
		basePath, secretsPath := writeEnv(t, fullBaseEnv, fullSecretsEnv)
		env, err := Generate(Options{BaseFile: basePath, SecretsFile: secretsPath, Context: "dev"})
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if _, ok := env["REDIS_URL"]; ok {
			t.Fatalf("REDIS_URL should be absent, got %q", env["REDIS_URL"])
		}
	})
}

func TestSetGRPCAddrDerivation(t *testing.T) {
	t.Run("happy path derives all grpc addrs", func(t *testing.T) {
		basePath, secretsPath := writeEnv(t, fullBaseEnv, fullSecretsEnv)
		env, err := Generate(Options{BaseFile: basePath, SecretsFile: secretsPath, Context: "dev"})
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		want := map[string]string{
			"COMMODORE_GRPC_ADDR":     "commodore:19001",
			"QUARTERMASTER_GRPC_ADDR": "quartermaster:19002",
			"PURSER_GRPC_ADDR":        "purser:19003",
			"PERISCOPE_GRPC_ADDR":     "periscope-query:19004",
			"SIGNALMAN_GRPC_ADDR":     "signalman:19005",
			"DECKHAND_GRPC_ADDR":      "deckhand:19006",
			"SKIPPER_GRPC_ADDR":       "skipper:19007",
			"NAVIGATOR_GRPC_ADDR":     "navigator:18011",
			"DECKLOG_GRPC_ADDR":       "decklog:18006",
		}
		for k, v := range want {
			if got := env[k]; got != v {
				t.Fatalf("%s = %q, want %q", k, got, v)
			}
		}
	})

	t.Run("missing grpc host errors", func(t *testing.T) {
		base := strings.Replace(fullBaseEnv, "SIGNALMAN_HOST=signalman\n", "", 1)
		basePath, secretsPath := writeEnv(t, base, fullSecretsEnv)
		_, err := Generate(Options{BaseFile: basePath, SecretsFile: secretsPath, Context: "dev"})
		if err == nil {
			t.Fatal("expected error for missing SIGNALMAN_HOST")
		}
		if !strings.Contains(err.Error(), "SIGNALMAN_HOST is not set") {
			t.Fatalf("error = %q, want SIGNALMAN_HOST not set", err.Error())
		}
	})

	t.Run("missing grpc port errors", func(t *testing.T) {
		base := strings.Replace(fullBaseEnv, "SIGNALMAN_GRPC_PORT=19005\n", "", 1)
		basePath, secretsPath := writeEnv(t, base, fullSecretsEnv)
		_, err := Generate(Options{BaseFile: basePath, SecretsFile: secretsPath, Context: "dev"})
		if err == nil {
			t.Fatal("expected error for missing SIGNALMAN_GRPC_PORT")
		}
		if !strings.Contains(err.Error(), "SIGNALMAN_GRPC_PORT is not set") {
			t.Fatalf("error = %q, want SIGNALMAN_GRPC_PORT not set", err.Error())
		}
	})
}

func TestParseEnvLineNumbersInErrors(t *testing.T) {
	t.Run("invalid line reports its 1-based number", func(t *testing.T) {
		content := []byte("KEY1=value1\nKEY2=value2\nthisisnotvalid\n")
		_, err := parseEnv(content)
		if err == nil {
			t.Fatal("expected error")
		}
		if err.Error() != "invalid line 3" {
			t.Fatalf("error = %q, want %q", err.Error(), "invalid line 3")
		}
	})

	t.Run("empty key reports its 1-based line number", func(t *testing.T) {
		content := []byte("KEY1=value1\n=value2\n")
		_, err := parseEnv(content)
		if err == nil {
			t.Fatal("expected error")
		}
		if err.Error() != "empty key on line 2" {
			t.Fatalf("error = %q, want %q", err.Error(), "empty key on line 2")
		}
	})
}
