package llm

import (
	"fmt"
	"strings"
)

// Config selects and authenticates a model provider. Callers build it from
// their typed service configuration.
type Config struct {
	Provider  string
	Model     string
	APIKey    string
	APIURL    string
	MaxTokens int
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
