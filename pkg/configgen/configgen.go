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

func joinURLPath(baseURL, suffix string) (string, error) {
	if suffix == "" {
		return "", fmt.Errorf("suffix is required")
	}
	base := strings.TrimRight(baseURL, "/")
	if !strings.HasPrefix(suffix, "/") {
		suffix = "/" + suffix
	}
	return base + suffix, nil
}

func validateGatewayBaseURL(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse gateway URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("gateway URL must be http or https, got %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("gateway URL must include host")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("gateway URL must not include query or fragment")
	}

	normalizedPath := strings.TrimRight(parsed.Path, "/")
	if normalizedPath == "" {
		return strings.TrimRight(raw, "/"), nil
	}

	switch {
	case normalizedPath == "/graphql" || strings.HasSuffix(normalizedPath, "/graphql"):
		return "", fmt.Errorf("gateway URL must be base path only (cannot include /graphql)")
	case normalizedPath == "/graphql/ws" || strings.HasSuffix(normalizedPath, "/graphql/ws"):
		return "", fmt.Errorf("gateway URL must be base path only (cannot include /graphql/ws)")
	case normalizedPath == "/mcp" || strings.HasSuffix(normalizedPath, "/mcp"):
		return "", fmt.Errorf("gateway URL must be base path only (cannot include /mcp)")
	case normalizedPath == "/webhooks" || strings.HasSuffix(normalizedPath, "/webhooks"):
		return "", fmt.Errorf("gateway URL must be base path only (cannot include /webhooks)")
	}

	return strings.TrimRight(raw, "/"), nil
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

	if errSet := setHTTPURL(env, "COMMODORE_URL", "COMMODORE_HOST", "COMMODORE_PORT"); errSet != nil {
		return errSet
	}
	if errSet := setHTTPURL(env, "QUARTERMASTER_URL", "QUARTERMASTER_HOST", "QUARTERMASTER_PORT"); errSet != nil {
		return errSet
	}
	if errSet := setHTTPURL(env, "PURSER_URL", "PURSER_HOST", "PURSER_PORT"); errSet != nil {
		return errSet
	}
	if errSet := setHTTPURL(env, "PERISCOPE_QUERY_URL", "PERISCOPE_QUERY_HOST", "PERISCOPE_QUERY_PORT"); errSet != nil {
		return errSet
	}
	if errSet := setHTTPURL(env, "PERISCOPE_INGEST_URL", "PERISCOPE_INGEST_HOST", "PERISCOPE_INGEST_PORT"); errSet != nil {
		return errSet
	}
	if errSet := setGRPCAddr(env, "DECKLOG_GRPC_ADDR", "DECKLOG_HOST", "DECKLOG_PORT"); errSet != nil {
		return errSet
	}
	if errSet := setHTTPURL(env, "HELMSMAN_WEBHOOK_URL", "HELMSMAN_HOST", "HELMSMAN_PORT"); errSet != nil {
		return errSet
	}
	if errSet := setHTTPURL(env, "FOGHORN_URL", "FOGHORN_HOST", "FOGHORN_PORT"); errSet != nil {
		return errSet
	}
	if errSet := setHTTPURL(env, "MISTSERVER_URL", "MISTSERVER_HOST", "MISTSERVER_PORT"); errSet != nil {
		return errSet
	}

	// Navigator gRPC URL (no scheme, just host:port)
	navHost, err := require(env, "NAVIGATOR_HOST")
	if err != nil {
		return err
	}
	navGRPCPort, err := require(env, "NAVIGATOR_GRPC_PORT")
	if err != nil {
		return err
	}
	env["NAVIGATOR_URL"] = fmt.Sprintf("%s:%s", navHost, navGRPCPort)

	// Control Plane gRPC addresses (host:port, no scheme)
	// These are used for internal service-to-service communication
	if errSet := setGRPCAddr(env, "COMMODORE_GRPC_ADDR", "COMMODORE_HOST", "COMMODORE_GRPC_PORT"); errSet != nil {
		return errSet
	}
	if errSet := setGRPCAddr(env, "QUARTERMASTER_GRPC_ADDR", "QUARTERMASTER_HOST", "QUARTERMASTER_GRPC_PORT"); errSet != nil {
		return errSet
	}
	if errSet := setGRPCAddr(env, "PURSER_GRPC_ADDR", "PURSER_HOST", "PURSER_GRPC_PORT"); errSet != nil {
		return errSet
	}
	if errSet := setGRPCAddr(env, "PERISCOPE_GRPC_ADDR", "PERISCOPE_QUERY_HOST", "PERISCOPE_QUERY_GRPC_PORT"); errSet != nil {
		return errSet
	}
	if errSet := setGRPCAddr(env, "SIGNALMAN_GRPC_ADDR", "SIGNALMAN_HOST", "SIGNALMAN_GRPC_PORT"); errSet != nil {
		return errSet
	}
	if errSet := setGRPCAddr(env, "DECKHAND_GRPC_ADDR", "DECKHAND_HOST", "DECKHAND_GRPC_PORT"); errSet != nil {
		return errSet
	}
	if errSet := setGRPCAddr(env, "SKIPPER_GRPC_ADDR", "SKIPPER_HOST", "SKIPPER_GRPC_PORT"); errSet != nil {
		return errSet
	}

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

	// Foghorn HA via Redis (optional — empty means standalone mode)
	if redisURL := env["FOGHORN_REDIS_URL"]; redisURL != "" {
		env["REDIS_URL"] = redisURL
	}

	// WEBAPP_PUBLIC_URL is already defined in base.env, no derivation needed

	return nil
}

// computeViteVariables derives browser-accessible URLs from deployment configuration.
// These VITE_* variables are baked into the frontend build at compile time.
func computeViteVariables(env map[string]string) error {
	// 1. GATEWAY ENDPOINTS - Read from base.env (base URL only)
	gatewayBaseURL, err := require(env, "GATEWAY_PUBLIC_URL")
	if err != nil {
		return err
	}
	gatewayBaseURL, err = validateGatewayBaseURL(gatewayBaseURL)
	if err != nil {
		return fmt.Errorf("invalid GATEWAY_PUBLIC_URL: %w", err)
	}

	graphQLHTTPURL, err := joinURLPath(gatewayBaseURL, "/graphql")
	if err != nil {
		return fmt.Errorf("build GraphQL URL: %w", err)
	}
	env["VITE_GRAPHQL_HTTP_URL"] = graphQLHTTPURL
	env["VITE_GATEWAY_URL"] = gatewayBaseURL

	wsBase, err := convertToWS(gatewayBaseURL)
	if err != nil {
		return fmt.Errorf("convert gateway URL to WebSocket: %w", err)
	}
	graphQLWSURL, err := joinURLPath(wsBase, "/graphql/ws")
	if err != nil {
		return fmt.Errorf("build GraphQL WS URL: %w", err)
	}
	env["VITE_GRAPHQL_WS_URL"] = graphQLWSURL

	mcpURL, err := joinURLPath(gatewayBaseURL, "/mcp")
	if err != nil {
		return fmt.Errorf("build MCP URL: %w", err)
	}
	env["VITE_MCP_URL"] = mcpURL

	webhooksURL, err := joinURLPath(gatewayBaseURL, "/webhooks")
	if err != nil {
		return fmt.Errorf("build webhooks URL: %w", err)
	}
	env["VITE_WEBHOOKS_URL"] = webhooksURL

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

	marketingPublicURL, err := require(env, "MARKETING_PUBLIC_URL")
	if err != nil {
		return err
	}
	env["VITE_MARKETING_SITE_URL"] = marketingPublicURL

	docsPublicURL, err := require(env, "DOCS_PUBLIC_URL")
	if err != nil {
		return err
	}
	env["VITE_DOCS_SITE_URL"] = docsPublicURL

	// Contact API URL - Read from base.env
	formsPublicURL, err := require(env, "FORMS_PUBLIC_URL")
	if err != nil {
		return err
	}
	env["VITE_CONTACT_API_URL"] = formsPublicURL

	// 3. STREAMING - Pass through raw base.env values, let apps construct protocol-specific URLs
	// Apps parse these to derive hostname and construct rtmp://, srt://, https:// URLs as needed
	streamingIngestURL, err := require(env, "STREAMING_INGEST_URL")
	if err != nil {
		return err
	}
	env["VITE_STREAMING_INGEST_URL"] = streamingIngestURL

	streamingPlayURL, err := require(env, "STREAMING_PLAY_URL")
	if err != nil {
		return err
	}
	env["VITE_STREAMING_PLAY_URL"] = streamingPlayURL

	streamingEdgeURL, err := require(env, "STREAMING_EDGE_URL")
	if err != nil {
		return err
	}
	env["VITE_STREAMING_EDGE_URL"] = streamingEdgeURL

	// Streaming ports for protocols that need explicit ports (SRT, RTMP)
	env["VITE_STREAMING_RTMP_PORT"] = valueOrDefault(env, "STREAMING_RTMP_PORT", "1935")
	env["VITE_STREAMING_SRT_PORT"] = valueOrDefault(env, "STREAMING_SRT_PORT", "8889")

	// Streaming paths from base.env
	env["VITE_STREAMING_HLS_PATH"] = valueOrDefault(env, "STREAMING_HLS_PATH", "/hls")
	env["VITE_STREAMING_WEBRTC_PATH"] = valueOrDefault(env, "STREAMING_WEBRTC_PATH", "/webrtc")
	env["VITE_STREAMING_RTMP_PATH"] = valueOrDefault(env, "STREAMING_RTMP_PATH", "/live")
	env["VITE_STREAMING_EMBED_PATH"] = valueOrDefault(env, "STREAMING_EMBED_PATH", "/")

	// 5. BRANDING - Passthrough from base.env
	env["VITE_COMPANY_NAME"] = valueOrDefault(env, "BRAND_NAME", "FrameWorks")
	env["VITE_DOMAIN"] = valueOrDefault(env, "BRAND_DOMAIN", "frameworks.network")
	env["VITE_CONTACT_EMAIL"] = valueOrDefault(env, "BRAND_CONTACT_EMAIL", "info@frameworks.network")

	// 6. EXTERNAL LINKS - Passthrough from base.env
	githubURL, err := require(env, "GITHUB_URL")
	if err != nil {
		return err
	}
	env["VITE_GITHUB_URL"] = githubURL

	livepeerURL, err := require(env, "LIVEPEER_URL")
	if err != nil {
		return err
	}
	env["VITE_LIVEPEER_URL"] = livepeerURL

	livepeerExplorerURL, err := require(env, "LIVEPEER_EXPLORER_URL")
	if err != nil {
		return err
	}
	env["VITE_LIVEPEER_EXPLORER_URL"] = livepeerExplorerURL

	forumURL, err := require(env, "FORUM_URL")
	if err != nil {
		return err
	}
	env["VITE_FORUM_URL"] = forumURL

	discordURL, err := require(env, "DISCORD_URL")
	if err != nil {
		return err
	}
	env["VITE_DISCORD_URL"] = discordURL

	demoStreamName, err := require(env, "DEMO_STREAM_NAME")
	if err != nil {
		return err
	}
	env["VITE_DEMO_STREAM_NAME"] = demoStreamName

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

	// 9. DOCS CONFIG - Extract DOCS_BASE_PATH from DOCS_PUBLIC_URL
	var docsPath string
	docsPath, err = extractPathFromURL(docsPublicURL)
	if err != nil {
		return fmt.Errorf("extract path from docs URL: %w", err)
	}
	env["DOCS_BASE_PATH"] = docsPath

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

// setGRPCAddr derives a gRPC address (host:port, no scheme) from host and port env vars
func setGRPCAddr(env map[string]string, targetKey, hostKey, portKey string) error {
	host, err := require(env, hostKey)
	if err != nil {
		return err
	}
	port, err := require(env, portKey)
	if err != nil {
		return err
	}
	env[targetKey] = fmt.Sprintf("%s:%s", host, port)
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
