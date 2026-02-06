package config

import (
	"strconv"
	"strings"

	"frameworks/pkg/config"
)

// Config stores environment configuration for Skipper.
type Config struct {
	Port               string
	GRPCPort           string
	DatabaseURL        string
	LLMProvider        string
	LLMModel           string
	LLMAPIKey          string
	LLMAPIURL          string
	EmbeddingProvider  string
	EmbeddingModel     string
	EmbeddingAPIKey    string
	EmbeddingAPIURL    string
	SearchProvider     string
	SearchAPIKey       string
	SearchAPIURL       string
	RequiredTierLevel  int
	ChatRateLimitHour  int
	RateLimitOverrides map[string]int
	BillingKafkaTopic  string
	KafkaBrokers       []string
	KafkaClusterID     string
	GatewayPublicURL   string
}

// GatewayMCPURL returns the MCP endpoint URL derived from the gateway base.
// Returns empty string when GatewayPublicURL is unset.
func (c Config) GatewayMCPURL() string {
	if c.GatewayPublicURL == "" {
		return ""
	}
	return strings.TrimRight(c.GatewayPublicURL, "/") + "/mcp"
}

// LoadConfig loads the Skipper configuration from environment variables.
func LoadConfig() Config {
	brokersEnv := strings.TrimSpace(config.GetEnv("KAFKA_BROKERS", ""))
	var brokers []string
	if brokersEnv != "" {
		for _, broker := range strings.Split(brokersEnv, ",") {
			broker = strings.TrimSpace(broker)
			if broker != "" {
				brokers = append(brokers, broker)
			}
		}
	}
	rateLimitOverrides := parseRateLimitOverrides(config.GetEnv("SKIPPER_CHAT_RATE_LIMIT_OVERRIDES", ""))
	return Config{
		Port:               config.GetEnv("PORT", "18018"),
		GRPCPort:           config.GetEnv("GRPC_PORT", "19007"),
		DatabaseURL:        config.RequireEnv("DATABASE_URL"),
		LLMProvider:        config.GetEnv("LLM_PROVIDER", ""),
		LLMModel:           config.GetEnv("LLM_MODEL", ""),
		LLMAPIKey:          config.GetEnv("LLM_API_KEY", ""),
		LLMAPIURL:          config.GetEnv("LLM_API_URL", ""),
		EmbeddingProvider:  config.GetEnv("EMBEDDING_PROVIDER", config.GetEnv("LLM_PROVIDER", "")),
		EmbeddingModel:     config.GetEnv("EMBEDDING_MODEL", config.GetEnv("LLM_MODEL", "")),
		EmbeddingAPIKey:    config.GetEnv("EMBEDDING_API_KEY", config.GetEnv("LLM_API_KEY", "")),
		EmbeddingAPIURL:    config.GetEnv("EMBEDDING_API_URL", config.GetEnv("LLM_API_URL", "")),
		SearchProvider:     config.GetEnv("SEARCH_PROVIDER", ""),
		SearchAPIKey:       config.GetEnv("SEARCH_API_KEY", ""),
		SearchAPIURL:       config.GetEnv("SEARCH_API_URL", ""),
		RequiredTierLevel:  config.GetEnvInt("SKIPPER_REQUIRED_TIER_LEVEL", 3),
		ChatRateLimitHour:  config.GetEnvInt("SKIPPER_CHAT_RATE_LIMIT_PER_HOUR", 0),
		RateLimitOverrides: rateLimitOverrides,
		BillingKafkaTopic:  config.GetEnv("BILLING_KAFKA_TOPIC", "billing.usage_reports"),
		KafkaBrokers:       brokers,
		KafkaClusterID:     config.GetEnv("KAFKA_CLUSTER_ID", "local"),
		GatewayPublicURL:   config.GetEnv("GATEWAY_PUBLIC_URL", ""),
	}
}

func parseRateLimitOverrides(raw string) map[string]int {
	overrides := map[string]int{}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return overrides
	}
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.Split(entry, ":")
		if len(parts) != 2 {
			continue
		}
		tenantID := strings.TrimSpace(parts[0])
		if tenantID == "" {
			continue
		}
		limit, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil || limit < 0 {
			continue
		}
		overrides[tenantID] = limit
	}
	return overrides
}
