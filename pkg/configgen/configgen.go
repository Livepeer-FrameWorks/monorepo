package configgen

import (
	"bufio"
	"bytes"
	"fmt"
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

	chHost, err := require(env, "CLICKHOUSE_SERVICE_HOST")
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
	if err := setHTTPURL(env, "FOGHORN_HTTP_BASE", "FOGHORN_HOST", "FOGHORN_PORT"); err != nil {
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
