package search

import (
	"fmt"

	"frameworks/pkg/config"
)

const (
	providerTavily  = "tavily"
	providerBrave   = "brave"
	providerSearxng = "searxng"
)

// Config holds environment configuration for search providers.
type Config struct {
	Provider string
	APIKey   string
	APIURL   string
}

// LoadConfig loads search configuration from the environment.
func LoadConfig() Config {
	return Config{
		Provider: config.GetEnv("SEARCH_PROVIDER", providerTavily),
		APIKey:   config.GetEnv("SEARCH_API_KEY", ""),
		APIURL:   config.GetEnv("SEARCH_API_URL", ""),
	}
}

// NewProvider creates a search provider from configuration.
func NewProvider(cfg Config) (Provider, error) {
	switch cfg.Provider {
	case providerTavily:
		return NewTavilyProvider(cfg.APIKey, cfg.APIURL)
	case providerBrave:
		return NewBraveProvider(cfg.APIKey, cfg.APIURL)
	case providerSearxng:
		return NewSearxngProvider(cfg.APIURL)
	default:
		return nil, fmt.Errorf("unsupported search provider: %s", cfg.Provider)
	}
}
