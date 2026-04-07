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
WEBAPP_PUBLIC_URL=http://localhost:18090/app
MARKETING_PUBLIC_URL=http://localhost:18090/marketing
DOCS_PUBLIC_URL=http://localhost:18090/docs
FORMS_PUBLIC_URL=http://localhost:18032
STREAMING_INGEST_URL=http://localhost:8080
STREAMING_PLAY_URL=http://localhost:18008
STREAMING_EDGE_URL=http://localhost:18090/view
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

func TestGenerateFrontendOnlyEmits16Outputs(t *testing.T) {
	t.Setenv("TZ", "UTC")

	dir := t.TempDir()
	basePath := filepath.Join(dir, "frontend-base.env")

	base := `GATEWAY_PUBLIC_URL=https://bridge.frameworks.network
WEBAPP_PUBLIC_URL=https://chartroom.frameworks.network/app
MARKETING_PUBLIC_URL=https://frameworks.network
DOCS_PUBLIC_URL=https://logbook.frameworks.network
FORMS_PUBLIC_URL=https://steward.frameworks.network
STREAMING_INGEST_URL=https://edge-ingest.frameworks.network
STREAMING_PLAY_URL=https://foghorn.frameworks.network
STREAMING_EDGE_URL=https://edge-egress.frameworks.network
FROM_EMAIL=info@frameworks.network
TURNSTILE_AUTH_SITE_KEY=site-auth
TURNSTILE_FORMS_SITE_KEY=site-forms
`

	if err := os.WriteFile(basePath, []byte(base), 0o600); err != nil {
		t.Fatalf("write base env: %v", err)
	}

	env, err := Generate(Options{
		BaseFile:     basePath,
		Context:      "production",
		FrontendOnly: true,
	})
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}

	// 16 generated outputs + ENV_CONTEXT + ENV_GENERATED_AT = 18 keys
	want := map[string]string{
		"VITE_GATEWAY_URL":              "https://bridge.frameworks.network",
		"VITE_GRAPHQL_HTTP_URL":         "https://bridge.frameworks.network/graphql",
		"VITE_GRAPHQL_WS_URL":           "wss://bridge.frameworks.network/graphql/ws",
		"VITE_AUTH_URL":                 "https://bridge.frameworks.network/auth",
		"VITE_APP_URL":                  "https://chartroom.frameworks.network/app",
		"VITE_MARKETING_SITE_URL":       "https://frameworks.network",
		"VITE_DOCS_SITE_URL":            "https://logbook.frameworks.network",
		"VITE_CONTACT_API_URL":          "https://steward.frameworks.network",
		"VITE_STREAMING_INGEST_URL":     "https://edge-ingest.frameworks.network",
		"VITE_STREAMING_PLAY_URL":       "https://foghorn.frameworks.network",
		"VITE_STREAMING_EDGE_URL":       "https://edge-egress.frameworks.network",
		"VITE_TURNSTILE_AUTH_SITE_KEY":  "site-auth",
		"VITE_TURNSTILE_FORMS_SITE_KEY": "site-forms",
		"VITE_CONTACT_EMAIL":            "info@frameworks.network",
		"VITE_MCP_URL":                  "https://bridge.frameworks.network/mcp",
		"VITE_WEBHOOKS_URL":             "https://bridge.frameworks.network/webhooks",
	}
	for key, expected := range want {
		if got := env[key]; got != expected {
			t.Errorf("%s = %q, want %q", key, got, expected)
		}
	}

	// Deleted outputs must be absent
	deleted := []string{
		"BASE_PATH", "DOCS_BASE_PATH",
		"VITE_STREAMING_RTMP_PORT", "VITE_STREAMING_SRT_PORT",
		"VITE_STREAMING_RTMP_PATH", "VITE_STREAMING_HLS_PATH",
		"VITE_STREAMING_WEBRTC_PATH", "VITE_STREAMING_EMBED_PATH",
		"VITE_GITHUB_URL", "VITE_LIVEPEER_URL", "VITE_LIVEPEER_EXPLORER_URL",
		"VITE_FORUM_URL", "VITE_DISCORD_URL", "VITE_TWITTER_URL",
		"VITE_DEMO_STREAM_NAME", "VITE_COMPANY_NAME", "VITE_DOMAIN",
		"DATABASE_URL", "JWT_SECRET",
	}
	for _, key := range deleted {
		if _, ok := env[key]; ok {
			t.Errorf("%s should be absent from frontend env", key)
		}
	}
}

func TestOverlayPrecedence(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, "base.env")
	overlayPath := filepath.Join(dir, "overlay.env")

	base := `GATEWAY_PUBLIC_URL=http://localhost:18090
WEBAPP_PUBLIC_URL=http://localhost:18090/app
MARKETING_PUBLIC_URL=http://localhost:18090/marketing
DOCS_PUBLIC_URL=http://localhost:18090/docs
FORMS_PUBLIC_URL=http://localhost:18032
STREAMING_INGEST_URL=http://localhost:8080
STREAMING_PLAY_URL=http://localhost:18008
STREAMING_EDGE_URL=http://localhost:18090/view
FROM_EMAIL=dev@localhost
`
	overlay := `GATEWAY_PUBLIC_URL=https://bridge.prod.example.com
FROM_EMAIL=info@prod.example.com
TURNSTILE_AUTH_SITE_KEY=prod-key
`

	os.WriteFile(basePath, []byte(base), 0o600)
	os.WriteFile(overlayPath, []byte(overlay), 0o600)

	env, err := Generate(Options{
		BaseFile:     basePath,
		OverlayFiles: []string{overlayPath},
		Context:      "production",
		FrontendOnly: true,
	})
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}

	// Overlay wins
	if got := env["VITE_GATEWAY_URL"]; got != "https://bridge.prod.example.com" {
		t.Errorf("VITE_GATEWAY_URL = %q, want overlay value", got)
	}
	if got := env["VITE_CONTACT_EMAIL"]; got != "info@prod.example.com" {
		t.Errorf("VITE_CONTACT_EMAIL = %q, want overlay value", got)
	}
	if got := env["VITE_TURNSTILE_AUTH_SITE_KEY"]; got != "prod-key" {
		t.Errorf("VITE_TURNSTILE_AUTH_SITE_KEY = %q, want overlay value", got)
	}
	// Base value not overridden
	if got := env["VITE_STREAMING_INGEST_URL"]; got != "http://localhost:8080" {
		t.Errorf("VITE_STREAMING_INGEST_URL = %q, want base value", got)
	}
}

func TestAuthURLDerivedFromGateway(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, "base.env")

	// No AUTH_PUBLIC_URL set — should derive from GATEWAY_PUBLIC_URL
	base := `GATEWAY_PUBLIC_URL=https://api.example.com
WEBAPP_PUBLIC_URL=https://app.example.com
MARKETING_PUBLIC_URL=https://example.com
DOCS_PUBLIC_URL=https://docs.example.com
FORMS_PUBLIC_URL=https://forms.example.com
STREAMING_INGEST_URL=https://ingest.example.com
STREAMING_PLAY_URL=https://play.example.com
STREAMING_EDGE_URL=https://edge.example.com
`

	os.WriteFile(basePath, []byte(base), 0o600)

	env, err := Generate(Options{
		BaseFile:     basePath,
		Context:      "test",
		FrontendOnly: true,
	})
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}

	if got := env["VITE_AUTH_URL"]; got != "https://api.example.com/auth" {
		t.Errorf("VITE_AUTH_URL = %q, want derived from gateway", got)
	}
}
