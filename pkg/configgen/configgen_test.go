package configgen

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateDerivesClickHouseAddrAndNavigatorGRPCAddr(t *testing.T) {
	t.Setenv("TZ", "UTC")

	dir := t.TempDir()
	basePath := filepath.Join(dir, "base.env")
	secretsPath := filepath.Join(dir, "secrets.env")

	base := `POSTGRES_HOST=postgres
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
FOGHORN_CONTROL_PORT=18019
MISTSERVER_HOST=mistserver
MISTSERVER_PORT=4242
NAVIGATOR_HOST=navigator
NAVIGATOR_GRPC_PORT=18011
GATEWAY_PUBLIC_URL=http://localhost:18090
AUTH_PUBLIC_URL=http://localhost:18090/auth
WEBAPP_PUBLIC_URL=http://localhost:18090/app
MARKETING_PUBLIC_URL=http://localhost:18090/marketing
DOCS_PUBLIC_URL=http://localhost:18090/docs
FORMS_PUBLIC_URL=http://localhost:18032
STREAMING_INGEST_URL=http://localhost:8080
STREAMING_PLAY_URL=http://localhost:18008
STREAMING_EDGE_URL=http://localhost:18090/view
GITHUB_URL=https://github.com/livepeer-frameworks/monorepo
LIVEPEER_URL=https://livepeer.org
LIVEPEER_EXPLORER_URL=https://explorer.livepeer.org
FORUM_URL=https://forum.frameworks.network
DISCORD_URL=https://discord.gg/9J6haUjdAq
DEMO_STREAM_NAME=live+frameworks-demo
`

	secrets := `POSTGRES_USER=frameworks_user
POSTGRES_PASSWORD=change-me
CLICKHOUSE_USER=frameworks
CLICKHOUSE_PASSWORD=change-me
JWT_SECRET=change-me
SERVICE_TOKEN=change-me
`

	if err := os.WriteFile(basePath, []byte(base), 0600); err != nil {
		t.Fatalf("write base env: %v", err)
	}
	if err := os.WriteFile(secretsPath, []byte(secrets), 0600); err != nil {
		t.Fatalf("write secrets env: %v", err)
	}

	env, err := Generate(Options{
		BaseFile:    basePath,
		SecretsFile: secretsPath,
		Context:     "test",
	})
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}

	if got := env["CLICKHOUSE_HOST"]; got != "clickhouse" {
		t.Fatalf("expected CLICKHOUSE_HOST to remain canonical host, got %q", got)
	}
	if got := env["CLICKHOUSE_ADDR"]; got != "clickhouse:9000" {
		t.Fatalf("expected CLICKHOUSE_ADDR to be derived, got %q", got)
	}
	if got := env["NAVIGATOR_GRPC_ADDR"]; got != "navigator:18011" {
		t.Fatalf("expected NAVIGATOR_GRPC_ADDR to be derived, got %q", got)
	}
	if _, ok := env["NAVIGATOR_URL"]; ok {
		t.Fatalf("expected NAVIGATOR_URL to be absent from generated env")
	}
}
