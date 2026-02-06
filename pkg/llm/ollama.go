package llm

import (
	"context"
	"strings"
)

type OllamaProvider struct {
	openai *OpenAIProvider
}

func NewOllamaProvider(cfg Config) *OllamaProvider {
	cfgCopy := cfg
	if strings.TrimSpace(cfgCopy.APIURL) == "" {
		cfgCopy.APIURL = "http://localhost:11434/v1"
	}
	return &OllamaProvider{
		openai: NewOpenAIProvider(cfgCopy),
	}
}

func (p *OllamaProvider) Complete(ctx context.Context, messages []Message, tools []Tool) (Stream, error) {
	return p.openai.Complete(ctx, messages, tools)
}
