package llm

import (
	"fmt"
	"strings"

	"frameworks/pkg/config"
)

type Config struct {
	Provider  string
	Model     string
	APIKey    string
	APIURL    string
	MaxTokens int
}

func LoadConfig() Config {
	return Config{
		Provider: config.GetEnv("LLM_PROVIDER", "openai"),
		Model:    config.GetEnv("LLM_MODEL", ""),
		APIKey:   config.GetEnv("LLM_API_KEY", ""),
		APIURL:   config.GetEnv("LLM_API_URL", ""),
	}
}

// LoadEmbeddingConfig loads embedding-specific configuration from EMBEDDING_*
// env vars, falling back to their LLM_* counterparts when unset.
func LoadEmbeddingConfig() Config {
	return Config{
		Provider: config.GetEnv("EMBEDDING_PROVIDER", config.GetEnv("LLM_PROVIDER", "openai")),
		Model:    config.GetEnv("EMBEDDING_MODEL", config.GetEnv("LLM_MODEL", "")),
		APIKey:   config.GetEnv("EMBEDDING_API_KEY", config.GetEnv("LLM_API_KEY", "")),
		APIURL:   config.GetEnv("EMBEDDING_API_URL", config.GetEnv("LLM_API_URL", "")),
	}
}

func NewProvider(cfg Config) (Provider, error) {
	switch strings.ToLower(cfg.Provider) {
	case "openai":
		return NewOpenAIProvider(cfg), nil
	case "anthropic":
		return NewAnthropicProvider(cfg), nil
	case "ollama":
		return NewOllamaProvider(cfg), nil
	default:
		return nil, fmt.Errorf("unknown LLM provider %q", cfg.Provider)
	}
}
