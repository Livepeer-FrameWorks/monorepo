package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Config is Skipper's resolved runtime configuration. The typed startup
// configuration in internal/appconfig builds it after applying the provider
// fallbacks and parsing the structured values.
type Config struct {
	DatabaseURL         string
	LLMProvider         string
	LLMModel            string
	LLMAPIKey           string
	LLMAPIURL           string
	LLMMaxTokens        int
	LLMContextWindow    int
	PromptTokenBudget   int
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
	GatewayPublicURL    string
	GatewayMCPURL       string
	GatewayMCPURLs      []string
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
	CrawlOriginRewrites map[string]string
	SocialEnabled       bool
	SocialInterval      time.Duration
	SocialMaxPerDay     int
	SocialNotifyEmail   string
}

// GatewayMCPEndpoint returns the MCP endpoint URL. Internal deployments can
// set GatewayMCPURL to bypass public DNS and reach Bridge over the mesh.
func (c Config) GatewayMCPEndpoint() string {
	endpoints := c.GatewayMCPEndpoints()
	if len(endpoints) > 0 {
		return endpoints[0]
	}
	return ""
}

func (c Config) GatewayMCPEndpoints() []string {
	if len(c.GatewayMCPURLs) > 0 {
		return normalizeURLList(c.GatewayMCPURLs)
	}
	if c.GatewayMCPURL != "" {
		return normalizeURLList([]string{c.GatewayMCPURL})
	}
	if c.GatewayPublicURL == "" {
		return nil
	}
	return []string{strings.TrimRight(c.GatewayPublicURL, "/") + "/mcp"}
}

// ParseOriginRewrites parses a JSON object of public origins to fetch origins
// and normalizes both sides to lower-case scheme://host. Empty input yields a
// nil map.
func ParseOriginRewrites(raw string) (map[string]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var configured map[string]string
	if err := json.Unmarshal([]byte(raw), &configured); err != nil {
		return nil, fmt.Errorf("must be a JSON object of origin to origin: %w", err)
	}
	rewrites := make(map[string]string, len(configured))
	for source, target := range configured {
		sourceOrigin, err := normalizeOrigin(source)
		if err != nil {
			return nil, fmt.Errorf("source %q: %w", source, err)
		}
		targetOrigin, err := normalizeOrigin(target)
		if err != nil {
			return nil, fmt.Errorf("target %q: %w", target, err)
		}
		rewrites[sourceOrigin] = targetOrigin
	}
	return rewrites, nil
}

func normalizeOrigin(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errors.New("origin must use http or https and include a host")
	}
	if u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("origin must not include credentials, a path, query, or fragment")
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host), nil
}

func normalizeURLList(urls []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(urls))
	for _, raw := range urls {
		u := strings.TrimRight(strings.TrimSpace(raw), "/")
		if u == "" {
			continue
		}
		if _, ok := seen[u]; ok {
			continue
		}
		seen[u] = struct{}{}
		result = append(result, u)
	}
	return result
}

// ParseRateLimitOverrides parses comma-separated tenant:limit pairs. A
// malformed, tenantless, or negative entry is skipped so the remaining
// overrides still apply.
func ParseRateLimitOverrides(raw string) map[string]int {
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
