package configgen

import (
	"bufio"
	"bytes"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Options struct {
	BaseFile    string
	SecretsFile string
	OutputFile  string
	Context     string
}

func (o *Options) defaults() error {
	if o.BaseFile == "" {
		return fmt.Errorf("BaseFile is required")
	}
	if o.SecretsFile == "" {
		return fmt.Errorf("SecretsFile is required")
	}
	if o.Context == "" {
		o.Context = "dev"
	}
	return nil
}

// convertToWS converts http/https URLs to ws/wss
func convertToWS(httpURL string) (string, error) {
	parsed, err := url.Parse(httpURL)
	if err != nil {
		return "", fmt.Errorf("parse URL: %w", err)
	}
	switch parsed.Scheme {
	case "http":
		parsed.Scheme = "ws"
	case "https":
		parsed.Scheme = "wss"
	default:
		return "", fmt.Errorf("unsupported scheme %q for WebSocket conversion", parsed.Scheme)
	}
	return parsed.String(), nil
}

// extractHostFromURL returns the host:port from a URL
func extractHostFromURL(rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse URL: %w", err)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("no host in URL %q", rawURL)
	}
	return parsed.Host, nil
}

// extractPathFromURL returns the path from a URL (defaults to "/" if empty)
func extractPathFromURL(rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse URL: %w", err)
	}
	if parsed.Path == "" {
		return "/", nil
	}
	return parsed.Path, nil
}

// Generate merges base + secrets env files, derives additional values, validates required variables,
// writes the output file if OutputFile is set, and returns the final environment map.
func Generate(opts Options) (map[string]string, error) {
	if err := opts.defaults(); err != nil {
		return nil, err
	}

	baseEnv, err := readEnvFile(opts.BaseFile)
	if err != nil {
		return nil, fmt.Errorf("read base env: %w", err)
	}

	secretsEnv, err := readEnvFile(opts.SecretsFile)
	if err != nil {
		return nil, fmt.Errorf("read secrets env: %w", err)
	}

	env := make(map[string]string, len(baseEnv)+len(secretsEnv)+32)
	merge(env, baseEnv)
	merge(env, secretsEnv)

	if err := computeDerived(env); err != nil {
		return nil, fmt.Errorf("derive values: %w", err)
	}

	if err := computeViteVariables(env); err != nil {
		return nil, fmt.Errorf("derive VITE variables: %w", err)
	}

	if err := validate(env); err != nil {
		return nil, err
	}

	env["ENV_CONTEXT"] = opts.Context
	env["ENV_GENERATED_AT"] = time.Now().UTC().Format(time.RFC3339)

	if opts.OutputFile != "" {
		if err := writeEnvFile(opts.OutputFile, env); err != nil {
			return nil, fmt.Errorf("write env file: %w", err)
		}
	}

	return env, nil
}

func readEnvFile(path string) (map[string]string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	data, err := parseEnv(content)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	return data, nil
}

func parseEnv(content []byte) (map[string]string, error) {
	result := make(map[string]string)
	scanner := bufio.NewScanner(bytes.NewReader(content))
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid line %d", lineNo)
		}
		key := strings.TrimSpace(parts[0])
		if key == "" {
			return nil, fmt.Errorf("empty key on line %d", lineNo)
		}
		value := strings.TrimSpace(parts[1])
		value = strings.Trim(value, "\"'")
		result[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func merge(dst map[string]string, src map[string]string) {
	for k, v := range src {
		dst[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
}

func computeDerived(env map[string]string) error {
	if strings.TrimSpace(env["WEBAPP_PORT"]) == "" {
		env["WEBAPP_PORT"] = "18030"
	}
	if strings.TrimSpace(env["WEBSITE_PORT"]) == "" {
		env["WEBSITE_PORT"] = "18031"
	}

	pgUser, err := require(env, "POSTGRES_USER")
	if err != nil {
		return err
	}
	pgPass, err := require(env, "POSTGRES_PASSWORD")
	if err != nil {
		return err
	}
	pgHost, err := require(env, "POSTGRES_HOST")
	if err != nil {
		return err
	}
	pgPort, err := require(env, "POSTGRES_PORT")
	if err != nil {
		return err
	}
	pgDB, err := require(env, "POSTGRES_DB")
	if err != nil {
		return err
	}
	sslMode := valueOrDefault(env, "POSTGRES_SSL_MODE", "disable")

	pgURL := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=%s", pgUser, pgPass, pgHost, pgPort, pgDB, sslMode)
	env["DATABASE_URL"] = pgURL

	kafkaHost, err := require(env, "KAFKA_HOST")
	if err != nil {
		return err
	}
	kafkaPort, err := require(env, "KAFKA_PORT")
	if err != nil {
		return err
	}
	env["KAFKA_BROKERS"] = fmt.Sprintf("%s:%s", kafkaHost, kafkaPort)

	chHost, err := require(env, "CLICKHOUSE_HOST")
	if err != nil {
		return err
	}
	chHTTPPort, err := require(env, "CLICKHOUSE_HTTP_PORT")
	if err != nil {
		return err
	}
	chNativePort, err := require(env, "CLICKHOUSE_NATIVE_PORT")
	if err != nil {
		return err
	}
	env["CLICKHOUSE_HOST"] = fmt.Sprintf("%s:%s", chHost, chNativePort)
	env["CLICKHOUSE_PORT"] = chHTTPPort

	if err := setHTTPURL(env, "COMMODORE_URL", "COMMODORE_HOST", "COMMODORE_PORT"); err != nil {
		return err
	}
	if err := setHTTPURL(env, "QUARTERMASTER_URL", "QUARTERMASTER_HOST", "QUARTERMASTER_PORT"); err != nil {
		return err
	}
	if err := setHTTPURL(env, "PURSER_URL", "PURSER_HOST", "PURSER_PORT"); err != nil {
		return err
	}
	if err := setHTTPURL(env, "PERISCOPE_QUERY_URL", "PERISCOPE_QUERY_HOST", "PERISCOPE_QUERY_PORT"); err != nil {
		return err
	}
	if err := setHTTPURL(env, "PERISCOPE_INGEST_URL", "PERISCOPE_INGEST_HOST", "PERISCOPE_INGEST_PORT"); err != nil {
		return err
	}
	if err := setHTTPURL(env, "DECKLOG_URL", "DECKLOG_HOST", "DECKLOG_PORT"); err != nil {
		return err
	}
	if err := setHTTPURL(env, "HELMSMAN_WEBHOOK_URL", "HELMSMAN_HOST", "HELMSMAN_PORT"); err != nil {
		return err
	}
	if err := setHTTPURL(env, "FOGHORN_URL", "FOGHORN_HOST", "FOGHORN_PORT"); err != nil {
		return err
	}
	if err := setHTTPURL(env, "MISTSERVER_URL", "MISTSERVER_HOST", "MISTSERVER_PORT"); err != nil {
		return err
	}

	signalmanHost, err := require(env, "SIGNALMAN_HOST")
	if err != nil {
		return err
	}
	signalmanPort, err := require(env, "SIGNALMAN_PORT")
	if err != nil {
		return err
	}
	env["SIGNALMAN_WS_URL"] = fmt.Sprintf("ws://%s:%s", signalmanHost, signalmanPort)

	foghornControlPort, err := require(env, "FOGHORN_CONTROL_PORT")
	if err != nil {
		return err
	}
	foghornHost, err := require(env, "FOGHORN_HOST")
	if err != nil {
		return err
	}
	env["FOGHORN_CONTROL_BIND_ADDR"] = fmt.Sprintf(":%s", foghornControlPort)
	env["FOGHORN_CONTROL_ADDR"] = fmt.Sprintf("%s:%s", foghornHost, foghornControlPort)

	// WEBAPP_PUBLIC_URL is already defined in base.env, no derivation needed

	return nil
}

// computeViteVariables derives browser-accessible URLs from deployment configuration.
// These VITE_* variables are baked into the frontend build at compile time.
func computeViteVariables(env map[string]string) error {
	// 1. GATEWAY ENDPOINTS - Read from base.env
	gatewayPublicURL, err := require(env, "GATEWAY_PUBLIC_URL")
	if err != nil {
		return err
	}

	env["VITE_GRAPHQL_HTTP_URL"] = gatewayPublicURL + "/"
	env["VITE_GATEWAY_URL"] = gatewayPublicURL + "/"

	wsURL, err := convertToWS(gatewayPublicURL)
	if err != nil {
		return fmt.Errorf("convert gateway URL to WebSocket: %w", err)
	}
	env["VITE_GRAPHQL_WS_URL"] = wsURL + "/ws"

	authPublicURL, err := require(env, "AUTH_PUBLIC_URL")
	if err != nil {
		return err
	}
	env["VITE_AUTH_URL"] = authPublicURL

	// 2. APPLICATION URLS - Read from base.env
	webappPublicURL, err := require(env, "WEBAPP_PUBLIC_URL")
	if err != nil {
		return err
	}
	env["VITE_APP_URL"] = webappPublicURL

	marketingPublicURL := valueOrDefault(env, "MARKETING_PUBLIC_URL", "http://localhost:18031")
	env["VITE_MARKETING_SITE_URL"] = marketingPublicURL

	// Contact API URL - Read from base.env
	formsPublicURL, err := require(env, "FORMS_PUBLIC_URL")
	if err != nil {
		return err
	}
	env["VITE_CONTACT_API_URL"] = formsPublicURL

	// 3. STREAMING - INGEST (Parse STREAMING_INGEST_URL for RTMP)
	streamingIngestURL := valueOrDefault(env, "STREAMING_INGEST_URL", "rtmp://localhost:1935/live")

	rtmpHost, err := extractHostFromURL(streamingIngestURL)
	if err != nil {
		return fmt.Errorf("extract host from streaming ingest URL: %w", err)
	}
	env["VITE_RTMP_DOMAIN"] = rtmpHost

	rtmpPath, err := extractPathFromURL(streamingIngestURL)
	if err != nil {
		return fmt.Errorf("extract path from streaming ingest URL: %w", err)
	}
	env["VITE_RTMP_PATH"] = rtmpPath

	// 4. STREAMING - EDGE/DELIVERY (Parse STREAMING_EDGE_URL for HTTP)
	streamingEdgeURL := valueOrDefault(env, "STREAMING_EDGE_URL", "http://localhost:18090/view")

	edgeHost, err := extractHostFromURL(streamingEdgeURL)
	if err != nil {
		return fmt.Errorf("extract host from streaming edge URL: %w", err)
	}
	env["VITE_HTTP_DOMAIN"] = edgeHost
	env["VITE_CDN_DOMAIN"] = edgeHost

	// Streaming paths from base.env
	env["VITE_HLS_PATH"] = valueOrDefault(env, "STREAMING_HLS_PATH", "/hls")
	env["VITE_WEBRTC_PATH"] = valueOrDefault(env, "STREAMING_WEBRTC_PATH", "/webrtc")
	env["VITE_EMBED_PATH"] = valueOrDefault(env, "STREAMING_EMBED_PATH", "/")

	// 5. BRANDING - Passthrough from base.env
	env["VITE_COMPANY_NAME"] = valueOrDefault(env, "BRAND_NAME", "FrameWorks")
	env["VITE_DOMAIN"] = valueOrDefault(env, "BRAND_DOMAIN", "frameworks.network")
	env["VITE_CONTACT_EMAIL"] = valueOrDefault(env, "BRAND_CONTACT_EMAIL", "info@frameworks.network")

	// 6. EXTERNAL LINKS - Passthrough from base.env
	env["VITE_GITHUB_URL"] = valueOrDefault(env, "GITHUB_URL", "https://github.com/livepeer-frameworks/monorepo")
	env["VITE_LIVEPEER_URL"] = valueOrDefault(env, "LIVEPEER_URL", "https://livepeer.org")
	env["VITE_LIVEPEER_EXPLORER_URL"] = valueOrDefault(env, "LIVEPEER_EXPLORER_URL", "https://explorer.livepeer.org")
	env["VITE_FORUM_URL"] = valueOrDefault(env, "FORUM_URL", "https://forum.frameworks.network")
	env["VITE_DISCORD_URL"] = valueOrDefault(env, "DISCORD_URL", "https://discord.gg/9J6haUjdAq")
	env["VITE_DEMO_STREAM_NAME"] = valueOrDefault(env, "DEMO_STREAM_NAME", "live+frameworks-demo")

	// 7. TURNSTILE - Passthrough from secrets.env
	if authKey := env["TURNSTILE_AUTH_SITE_KEY"]; authKey != "" {
		env["VITE_TURNSTILE_AUTH_SITE_KEY"] = authKey
	}
	if formsKey := env["TURNSTILE_FORMS_SITE_KEY"]; formsKey != "" {
		env["VITE_TURNSTILE_FORMS_SITE_KEY"] = formsKey
	}

	// 8. BUILD CONFIG - Extract BASE_PATH from WEBAPP_PUBLIC_URL
	webappPath, err := extractPathFromURL(webappPublicURL)
	if err != nil {
		return fmt.Errorf("extract path from webapp URL: %w", err)
	}
	env["BASE_PATH"] = webappPath

	return nil
}

func setHTTPURL(env map[string]string, targetKey, hostKey, portKey string) error {
	host, err := require(env, hostKey)
	if err != nil {
		return err
	}
	port, err := require(env, portKey)
	if err != nil {
		return err
	}
	env[targetKey] = fmt.Sprintf("http://%s:%s", host, port)
	return nil
}

func validate(env map[string]string) error {
	requiredKeys := []string{
		"POSTGRES_USER",
		"POSTGRES_PASSWORD",
		"POSTGRES_DB",
		"POSTGRES_HOST",
		"POSTGRES_PORT",
		"JWT_SECRET",
		"SERVICE_TOKEN",
		"CLICKHOUSE_DB",
		"CLICKHOUSE_USER",
		"CLICKHOUSE_PASSWORD",
	}

	var missing []string
	for _, key := range requiredKeys {
		if strings.TrimSpace(env[key]) == "" {
			missing = append(missing, key)
		}
	}

	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("missing required env vars: %s", strings.Join(missing, ", "))
	}
	return nil
}

func require(env map[string]string, key string) (string, error) {
	val := strings.TrimSpace(env[key])
	if val == "" {
		return "", fmt.Errorf("%s is not set", key)
	}
	return val, nil
}

func valueOrDefault(env map[string]string, key, def string) string {
	if val := strings.TrimSpace(env[key]); val != "" {
		return val
	}
	return def
}

func writeEnvFile(path string, env map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var builder strings.Builder
	builder.WriteString("# Generated by configgen on " + time.Now().UTC().Format(time.RFC3339) + "\n")
	for _, key := range keys {
		builder.WriteString(fmt.Sprintf("%s=%s\n", key, quote(env[key])))
	}

	return os.WriteFile(path, []byte(builder.String()), 0o600)
}

func quote(value string) string {
	return strconv.Quote(value)
}
