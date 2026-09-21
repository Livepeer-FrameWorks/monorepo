package bunny

import (
	"strings"
)

type Config struct {
	APIKey  string
	BaseURL string
}

// NewConfig returns the Bunny API configuration, or nil when apiKey is empty.
// An empty baseURL uses the public Bunny API.
func NewConfig(apiKey, baseURL string) *Config {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil
	}
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Config{
		APIKey:  apiKey,
		BaseURL: strings.TrimRight(baseURL, "/"),
	}
}

func NewClientFromConfig(cfg *Config) *Client {
	if cfg == nil {
		return nil
	}
	client := NewClient(cfg.APIKey)
	client.baseURL = cfg.BaseURL
	return client
}
