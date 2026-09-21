package search

import "fmt"

const (
	providerTavily  = "tavily"
	providerBrave   = "brave"
	providerSearxng = "searxng"
)

// Config selects and authenticates a search provider. Callers build it from
// their typed service configuration.
type Config struct {
	Provider string
	APIKey   string
	APIURL   string
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
