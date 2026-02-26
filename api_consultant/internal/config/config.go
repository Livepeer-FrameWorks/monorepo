package config

import (
	"strconv"
	"strings"
	"time"

	"frameworks/pkg/config"
)

// Config stores environment configuration for Skipper.
type Config struct {
	Port                string
	GRPCPort            string
	DatabaseURL         string
	LLMProvider         string
	LLMModel            string
	LLMAPIKey           string
	LLMAPIURL           string
	LLMMaxTokens        int
	EmbeddingProvider   string
	EmbeddingModel      string
	EmbeddingAPIKey     string
	EmbeddingAPIURL     string
	EmbeddingDimensions int
	SearchProvider      string
	SearchAPIKey        string
	SearchAPIURL        string
	RequiredTierLevel   int
	ChatRateLimitHour   int
	RateLimitOverrides  map[string]int
	BillingKafkaTopic   string
	KafkaBrokers        []string
	KafkaClusterID      string
	GatewayPublicURL    string
	AdminTenantID       string
	Sitemaps            []string
	SitemapsDir         string
	CrawlInterval       time.Duration
	SearchLimit         int
	MaxHistoryMessages  int
	ChunkTokenLimit     int
	ChunkTokenOverlap   int
	EnableRendering     bool
	UtilityLLMProvider  string
	UtilityLLMModel     string
	UtilityLLMAPIKey    string
	UtilityLLMAPIURL    string
	ContextualRetrieval bool
	LinkDiscovery       bool
	AdminAPIKey         string
	RerankProvider      string
	RerankModel         string
	RerankAPIKey        string
	RerankAPIURL        string
	EnableHyDE          bool
	SSRFAllowedHosts    []string
	SocialEnabled       bool
	SocialInterval      time.Duration
	SocialMaxPerDay     int
	SocialNotifyEmail   string
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
		Port:                config.GetEnv("PORT", "18018"),
		GRPCPort:            config.GetEnv("GRPC_PORT", "19007"),
		DatabaseURL:         config.RequireEnv("DATABASE_URL"),
		LLMProvider:         config.GetEnv("LLM_PROVIDER", ""),
		LLMModel:            config.GetEnv("LLM_MODEL", ""),
		LLMAPIKey:           config.GetEnv("LLM_API_KEY", ""),
		LLMAPIURL:           config.GetEnv("LLM_API_URL", ""),
		LLMMaxTokens:        config.GetEnvInt("LLM_MAX_TOKENS", 4096),
		EmbeddingProvider:   config.GetEnv("EMBEDDING_PROVIDER", config.GetEnv("LLM_PROVIDER", "")),
		EmbeddingModel:      config.GetEnv("EMBEDDING_MODEL", config.GetEnv("LLM_MODEL", "")),
		EmbeddingAPIKey:     config.GetEnv("EMBEDDING_API_KEY", config.GetEnv("LLM_API_KEY", "")),
		EmbeddingAPIURL:     config.GetEnv("EMBEDDING_API_URL", config.GetEnv("LLM_API_URL", "")),
		EmbeddingDimensions: config.GetEnvInt("EMBEDDING_DIMENSIONS", 0),
		SearchProvider:      config.GetEnv("SEARCH_PROVIDER", ""),
		SearchAPIKey:        config.GetEnv("SEARCH_API_KEY", ""),
		SearchAPIURL:        config.GetEnv("SEARCH_API_URL", ""),
		RequiredTierLevel:   config.GetEnvInt("SKIPPER_REQUIRED_TIER_LEVEL", 3),
		ChatRateLimitHour:   config.GetEnvInt("SKIPPER_CHAT_RATE_LIMIT_PER_HOUR", 0),
		RateLimitOverrides:  rateLimitOverrides,
		BillingKafkaTopic:   config.GetEnv("BILLING_KAFKA_TOPIC", "billing.usage_reports"),
		KafkaBrokers:        brokers,
		KafkaClusterID:      config.GetEnv("KAFKA_CLUSTER_ID", "local"),
		GatewayPublicURL:    config.GetEnv("GATEWAY_PUBLIC_URL", ""),
		AdminTenantID:       config.GetEnv("SKIPPER_ADMIN_TENANT_ID", ""),
		Sitemaps:            parseSitemapList(config.GetEnv("SITEMAPS", "")),
		SitemapsDir:         config.GetEnv("SKIPPER_SITEMAPS_DIR", ""),
		CrawlInterval:       parseDuration(config.GetEnv("CRAWL_INTERVAL", "24h"), 24*time.Hour),
		SearchLimit:         config.GetEnvInt("SKIPPER_SEARCH_LIMIT", 8),
		MaxHistoryMessages:  config.GetEnvInt("SKIPPER_MAX_HISTORY_MESSAGES", 20),
		ChunkTokenLimit:     config.GetEnvInt("CHUNK_TOKEN_LIMIT", 500),
		ChunkTokenOverlap:   config.GetEnvInt("CHUNK_TOKEN_OVERLAP", 50),
		EnableRendering:     config.GetEnv("SKIPPER_ENABLE_RENDERING", "") == "true",
		UtilityLLMProvider:  config.GetEnv("UTILITY_LLM_PROVIDER", config.GetEnv("LLM_PROVIDER", "")),
		UtilityLLMModel:     config.GetEnv("UTILITY_LLM_MODEL", config.GetEnv("LLM_MODEL", "")),
		UtilityLLMAPIKey:    config.GetEnv("UTILITY_LLM_API_KEY", config.GetEnv("LLM_API_KEY", "")),
		UtilityLLMAPIURL:    config.GetEnv("UTILITY_LLM_API_URL", config.GetEnv("LLM_API_URL", "")),
		ContextualRetrieval: config.GetEnv("SKIPPER_CONTEXTUAL_RETRIEVAL", "") == "true",
		LinkDiscovery:       config.GetEnv("SKIPPER_LINK_DISCOVERY", "") == "true",
		AdminAPIKey:         config.GetEnv("SKIPPER_API_KEY", ""),
		RerankProvider:      config.GetEnv("RERANKER_PROVIDER", ""),
		RerankModel:         config.GetEnv("RERANKER_MODEL", ""),
		RerankAPIKey:        config.GetEnv("RERANKER_API_KEY", config.GetEnv("LLM_API_KEY", "")),
		RerankAPIURL:        config.GetEnv("RERANKER_API_URL", ""),
		EnableHyDE:          config.GetEnv("SKIPPER_ENABLE_HYDE", "") == "true",
		SSRFAllowedHosts:    parseSitemapList(config.GetEnv("SKIPPER_SSRF_ALLOWED_HOSTS", "")),
		SocialEnabled:       config.GetEnv("SKIPPER_SOCIAL_ENABLED", "") == "true",
		SocialInterval:      parseDuration(config.GetEnv("SKIPPER_SOCIAL_INTERVAL", "2h"), 2*time.Hour),
		SocialMaxPerDay:     config.GetEnvInt("SKIPPER_SOCIAL_MAX_PER_DAY", 2),
		SocialNotifyEmail:   config.GetEnv("SKIPPER_SOCIAL_NOTIFY_EMAIL", ""),
	}
}

func parseSitemapList(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var result []string
	for _, item := range strings.Split(s, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			result = append(result, item)
		}
	}
	return result
}

func parseDuration(s string, fallback time.Duration) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil {
		return fallback
	}
	return d
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
