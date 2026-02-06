package llm

import (
	"os"
	"testing"
)

func TestLoadEmbeddingConfig_Defaults(t *testing.T) {
	for _, key := range []string{
		"LLM_PROVIDER", "LLM_MODEL", "LLM_API_KEY", "LLM_API_URL",
		"EMBEDDING_PROVIDER", "EMBEDDING_MODEL", "EMBEDDING_API_KEY", "EMBEDDING_API_URL",
	} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}

	cfg := LoadEmbeddingConfig()

	if cfg.Provider != "openai" {
		t.Errorf("Provider = %q, want %q", cfg.Provider, "openai")
	}
	if cfg.Model != "" {
		t.Errorf("Model = %q, want empty", cfg.Model)
	}
	if cfg.APIKey != "" {
		t.Errorf("APIKey = %q, want empty", cfg.APIKey)
	}
	if cfg.APIURL != "" {
		t.Errorf("APIURL = %q, want empty", cfg.APIURL)
	}
}

func TestLoadEmbeddingConfig_LLMFallback(t *testing.T) {
	t.Setenv("LLM_PROVIDER", "ollama")
	t.Setenv("LLM_MODEL", "llama3")
	t.Setenv("LLM_API_KEY", "sk-llm")
	t.Setenv("LLM_API_URL", "http://localhost:11434")

	cfg := LoadEmbeddingConfig()

	if cfg.Provider != "ollama" {
		t.Errorf("Provider = %q, want %q", cfg.Provider, "ollama")
	}
	if cfg.Model != "llama3" {
		t.Errorf("Model = %q, want %q", cfg.Model, "llama3")
	}
	if cfg.APIKey != "sk-llm" {
		t.Errorf("APIKey = %q, want %q", cfg.APIKey, "sk-llm")
	}
	if cfg.APIURL != "http://localhost:11434" {
		t.Errorf("APIURL = %q, want %q", cfg.APIURL, "http://localhost:11434")
	}
}

func TestLoadEmbeddingConfig_Override(t *testing.T) {
	t.Setenv("LLM_PROVIDER", "anthropic")
	t.Setenv("LLM_MODEL", "claude-sonnet-4-5-20250929")
	t.Setenv("LLM_API_KEY", "sk-ant")
	t.Setenv("LLM_API_URL", "")

	t.Setenv("EMBEDDING_PROVIDER", "openai")
	t.Setenv("EMBEDDING_MODEL", "text-embedding-3-small")
	t.Setenv("EMBEDDING_API_KEY", "sk-oai")
	t.Setenv("EMBEDDING_API_URL", "https://api.openai.com")

	cfg := LoadEmbeddingConfig()

	if cfg.Provider != "openai" {
		t.Errorf("Provider = %q, want %q", cfg.Provider, "openai")
	}
	if cfg.Model != "text-embedding-3-small" {
		t.Errorf("Model = %q, want %q", cfg.Model, "text-embedding-3-small")
	}
	if cfg.APIKey != "sk-oai" {
		t.Errorf("APIKey = %q, want %q", cfg.APIKey, "sk-oai")
	}
	if cfg.APIURL != "https://api.openai.com" {
		t.Errorf("APIURL = %q, want %q", cfg.APIURL, "https://api.openai.com")
	}
}
