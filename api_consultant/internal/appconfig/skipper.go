// Package appconfig holds the typed startup configuration of Skipper.
// scripts/configref generates the operator configuration reference from these
// structs.
package appconfig

import (
	"fmt"
	"time"

	skipperconfig "frameworks/api_consultant/internal/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
)

// Skipper is the startup configuration of the Skipper AI consultant. BUILD_ENV
// and email branding are read through config.Live at use time and follow a
// SIGHUP env-file reload; every other field applies at startup.
//
//configref:service skipper cmd=cmd/skipper
type Skipper struct {
	config.HTTPListen
	config.GRPCListen
	config.HTTPRuntime
	config.Logging
	config.GRPCMetadataPolicy
	config.SystemTenant
	config.ServiceAuth
	config.GRPCTLS
	config.Postgres
	config.Registration
	config.BuildEnvironment
	config.EmailBranding

	AdvertiseHost string `env:"SKIPPER_HOST" default:"skipper" desc:"Host name this instance advertises when it registers with Quartermaster." introduced:"v0.3.0"`
	Region        string `env:"REGION" desc:"Region label attached to usage events sent to Decklog." introduced:"v0.3.0"`

	PeriscopeGRPCAddr              string `env:"PERISCOPE_GRPC_ADDR" default:"periscope-query:19004" desc:"Periscope Query gRPC address for heartbeat diagnostics and infrastructure monitoring." introduced:"v0.3.0"`
	PeriscopeGRPCTLSServerName     string `env:"PERISCOPE_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Periscope Query connection. Empty uses the canonical internal name." introduced:"v0.3.0"`
	PurserGRPCAddr                 string `env:"PURSER_GRPC_ADDR" default:"purser:19003" desc:"Purser gRPC address for subscription tier checks and billing contacts." introduced:"v0.3.0"`
	PurserGRPCTLSServerName        string `env:"PURSER_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Purser connection. Empty uses the canonical internal name." introduced:"v0.3.0"`
	CommodoreGRPCAddr              string `env:"COMMODORE_GRPC_ADDR" default:"commodore:19001" desc:"Commodore gRPC address for tenant notification contacts and stream monitoring." introduced:"v0.3.0"`
	CommodoreGRPCTLSServerName     string `env:"COMMODORE_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Commodore connection. Empty uses the canonical internal name." introduced:"v0.3.0"`
	DecklogGRPCAddr                string `env:"DECKLOG_GRPC_ADDR" default:"decklog:18006" desc:"Decklog gRPC address for usage metering and websocket notifications." introduced:"v0.3.0"`
	DecklogGRPCTLSServerName       string `env:"DECKLOG_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Decklog connection. Empty uses the canonical internal name." introduced:"v0.3.0"`
	QuartermasterGRPCAddr          string `env:"QUARTERMASTER_GRPC_ADDR" default:"quartermaster:19002" desc:"Quartermaster gRPC address for tenant and cluster listings and service registration." introduced:"v0.3.0"`
	QuartermasterGRPCTLSServerName string `env:"QUARTERMASTER_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Quartermaster connection. Empty uses the canonical internal name." introduced:"v0.3.0"`
	LookoutGRPCAddr                string `env:"LOOKOUT_GRPC_ADDR" desc:"Lookout gRPC address used to attach investigation reports to incidents. Lookout-triggered investigations run only when this and KAFKA_BROKERS are set." introduced:"v0.3.8"`
	LookoutGRPCTLSServerName       string `env:"LOOKOUT_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Lookout connection. Empty uses the canonical internal name." introduced:"v0.3.8"`

	KafkaBrokers   []string `env:"KAFKA_BROKERS" desc:"Comma-separated bootstrap brokers of the aggregator Kafka cluster that carries the Lookout incident topic. Empty disables Lookout-triggered investigations." introduced:"v0.3.0"`
	KafkaClusterID string   `env:"KAFKA_CLUSTER_ID" default:"local" desc:"Kafka cluster identifier attached to the Lookout incident consumer." introduced:"v0.3.0"`

	LLMProvider       string `env:"LLM_PROVIDER" desc:"Chat model provider: openai, anthropic, or ollama. Empty or unknown leaves Skipper without a chat model." introduced:"v0.3.0"`
	LLMModel          string `env:"LLM_MODEL" desc:"Chat model name passed to LLM_PROVIDER." introduced:"v0.3.0"`
	LLMAPIKey         string `env:"LLM_API_KEY" secret:"true" desc:"API key for LLM_PROVIDER. Also the fallback key for embeddings, the utility model, and the reranker." introduced:"v0.3.0"`
	LLMAPIURL         string `env:"LLM_API_URL" desc:"Base URL override for LLM_PROVIDER. Empty uses the provider default." introduced:"v0.3.0"`
	LLMMaxTokens      int    `env:"LLM_MAX_TOKENS" default:"4096" desc:"Maximum tokens the chat model generates per response; also reserved when deriving the prompt token budget." introduced:"v0.3.0"`
	LLMContextWindow  int    `env:"LLM_CONTEXT_WINDOW" default:"0" desc:"Context window of the chat model in tokens. 0 uses the known window for recognized models." introduced:"v0.3.0"`
	PromptTokenBudget int    `env:"SKIPPER_PROMPT_TOKEN_BUDGET" default:"0" desc:"Explicit prompt token budget. 0 derives it from the context window and LLM_MAX_TOKENS." introduced:"v0.3.0"`

	EmbeddingProvider   string `env:"EMBEDDING_PROVIDER" desc:"Embedding provider for knowledge search. Empty uses LLM_PROVIDER." introduced:"v0.3.0"`
	EmbeddingModel      string `env:"EMBEDDING_MODEL" desc:"Embedding model name. Empty uses LLM_MODEL." introduced:"v0.3.0"`
	EmbeddingAPIKey     string `env:"EMBEDDING_API_KEY" secret:"true" desc:"API key for EMBEDDING_PROVIDER. Empty uses LLM_API_KEY." introduced:"v0.3.0"`
	EmbeddingAPIURL     string `env:"EMBEDDING_API_URL" desc:"Base URL override for EMBEDDING_PROVIDER. Empty uses LLM_API_URL." introduced:"v0.3.0"`
	EmbeddingDimensions int    `env:"EMBEDDING_DIMENSIONS" default:"0" desc:"Embedding vector size. 0 probes the embedding model at startup; a change truncates stored knowledge." introduced:"v0.3.0"`

	UtilityLLMProvider string `env:"UTILITY_LLM_PROVIDER" desc:"Provider of the utility model for query rewriting, HyDE, contextual retrieval, and social posts. Empty uses LLM_PROVIDER." introduced:"v0.3.0"`
	UtilityLLMModel    string `env:"UTILITY_LLM_MODEL" desc:"Utility model name. Empty uses LLM_MODEL." introduced:"v0.3.0"`
	UtilityLLMAPIKey   string `env:"UTILITY_LLM_API_KEY" secret:"true" desc:"API key for the utility model. Empty uses LLM_API_KEY; the utility model is disabled when both are empty." introduced:"v0.3.0"`
	UtilityLLMAPIURL   string `env:"UTILITY_LLM_API_URL" desc:"Base URL override for the utility model. Empty uses LLM_API_URL." introduced:"v0.3.0"`

	RerankerProvider string `env:"RERANKER_PROVIDER" desc:"Cross-encoder reranker provider. Empty ranks knowledge results with the keyword heuristic." introduced:"v0.3.0"`
	RerankerModel    string `env:"RERANKER_MODEL" desc:"Reranker model name passed to RERANKER_PROVIDER." introduced:"v0.3.0"`
	RerankerAPIKey   string `env:"RERANKER_API_KEY" secret:"true" desc:"API key for RERANKER_PROVIDER. Empty uses LLM_API_KEY." introduced:"v0.3.0"`
	RerankerAPIURL   string `env:"RERANKER_API_URL" desc:"Base URL override for RERANKER_PROVIDER." introduced:"v0.3.0"`

	SearchProvider string `env:"SEARCH_PROVIDER" desc:"Web search provider: tavily, brave, or searxng. Empty or unknown disables web search." introduced:"v0.3.0"`
	SearchAPIKey   string `env:"SEARCH_API_KEY" secret:"true" desc:"API key for SEARCH_PROVIDER." introduced:"v0.3.0"`
	SearchAPIURL   string `env:"SEARCH_API_URL" desc:"Base URL override for SEARCH_PROVIDER. Required by searxng." introduced:"v0.3.0"`
	SearchLimit    int    `env:"SKIPPER_SEARCH_LIMIT" default:"8" desc:"Maximum knowledge and web search results returned to the model per search." introduced:"v0.3.0"`

	RequiredTierLevel      int    `env:"SKIPPER_REQUIRED_TIER_LEVEL" default:"3" desc:"Minimum billing tier level for chat access and heartbeat analysis. 0 disables the tier check." introduced:"v0.3.0"`
	ChatRateLimitPerHour   int    `env:"SKIPPER_CHAT_RATE_LIMIT_PER_HOUR" default:"0" desc:"Chat requests allowed per tenant per hour. 0 or less means unlimited." introduced:"v0.3.0"`
	ChatRateLimitOverrides string `env:"SKIPPER_CHAT_RATE_LIMIT_OVERRIDES" desc:"Comma-separated tenant_id:limit pairs that replace the hourly chat limit for a tenant. Malformed or negative entries are ignored." introduced:"v0.3.0"`
	MaxHistoryMessages     int    `env:"SKIPPER_MAX_HISTORY_MESSAGES" default:"20" desc:"Conversation messages loaded as history for each chat turn." introduced:"v0.3.0"`

	GatewayPublicURL string   `env:"GATEWAY_PUBLIC_URL" desc:"Public Bridge URL. Its /mcp path is the platform tool endpoint when no GATEWAY_MCP_URL or GATEWAY_MCP_URLS is set." introduced:"v0.3.0"`
	GatewayMCPURL    string   `env:"GATEWAY_MCP_URL" desc:"Internal Bridge MCP endpoint for platform tools, used instead of the public URL." introduced:"v0.3.0"`
	GatewayMCPURLs   []string `env:"GATEWAY_MCP_URLS" desc:"Comma-separated Bridge MCP endpoints for platform tools. Takes precedence over GATEWAY_MCP_URL." introduced:"v0.3.0"`

	AdminTenantID string `env:"SKIPPER_ADMIN_TENANT_ID" desc:"Tenant the embedded web UI acts as. Empty uses the tenant ID local." introduced:"v0.3.0"`
	AdminAPIKey   string `env:"SKIPPER_API_KEY" secret:"true" desc:"Key that authenticates the embedded web UI and signs its session cookie." introduced:"v0.3.0"`
	WebUIEnabled  bool   `env:"SKIPPER_WEB_UI" default:"true" desc:"Serves the embedded web UI and its /admin/api routes." introduced:"v0.3.0"`
	WebUIInsecure bool   `env:"SKIPPER_WEB_UI_INSECURE" default:"false" desc:"Serves the embedded web UI without authentication when SKIPPER_API_KEY is empty. Otherwise the web UI stays disabled." introduced:"v0.3.0"`

	Sitemaps            []string      `env:"SITEMAPS" desc:"Comma-separated sitemap URLs crawled into the knowledge base on a schedule." introduced:"v0.3.0"`
	SitemapsDir         string        `env:"SKIPPER_SITEMAPS_DIR" desc:"Directory of sitemap files crawled into the knowledge base on a schedule." introduced:"v0.3.0"`
	DocsPublicURL       string        `env:"DOCS_PUBLIC_URL" desc:"Documentation base URL substituted into sitemap files." introduced:"v0.3.0"`
	MarketingPublicURL  string        `env:"MARKETING_PUBLIC_URL" desc:"Marketing base URL substituted into sitemap files." introduced:"v0.3.0"`
	CrawlInterval       time.Duration `env:"CRAWL_INTERVAL" default:"24h" desc:"Period over which the crawl scheduler revisits every known knowledge page." introduced:"v0.3.0"`
	ChunkTokenLimit     int           `env:"CHUNK_TOKEN_LIMIT" default:"500" desc:"Maximum tokens per embedded knowledge chunk. 0 or less keeps the embedder default." introduced:"v0.3.0"`
	ChunkTokenOverlap   int           `env:"CHUNK_TOKEN_OVERLAP" default:"50" desc:"Tokens shared between consecutive knowledge chunks. 0 or less keeps the embedder default." introduced:"v0.3.0"`
	EnableRendering     bool          `env:"SKIPPER_ENABLE_RENDERING" default:"false" desc:"Renders JavaScript pages with headless Chrome while crawling, when Chrome is available." introduced:"v0.3.0"`
	ContextualRetrieval bool          `env:"SKIPPER_CONTEXTUAL_RETRIEVAL" default:"false" desc:"Adds utility-model context summaries to crawled chunks before embedding. Needs a utility model." introduced:"v0.3.0"`
	LinkDiscovery       bool          `env:"SKIPPER_LINK_DISCOVERY" default:"false" desc:"Follows links found on crawled pages in addition to sitemap entries." introduced:"v0.3.0"`
	EnableHyDE          bool          `env:"SKIPPER_ENABLE_HYDE" default:"false" desc:"Enables hypothetical document embeddings for knowledge search. Needs a utility model and an embedder." introduced:"v0.3.0"`
	SSRFAllowedHosts    []string      `env:"SKIPPER_SSRF_ALLOWED_HOSTS" desc:"Comma-separated host names the crawler may fetch even when they resolve to private addresses." introduced:"v0.3.0"`
	CrawlOriginRewrites string        `env:"SKIPPER_CRAWL_ORIGIN_REWRITES" desc:"JSON object mapping public origins to the origins the crawler fetches instead. Page identities and citations keep the public URL." introduced:"v0.3.0"`

	HeartbeatInterval string `env:"HEARTBEAT_INTERVAL" default:"30m" desc:"Interval between heartbeat analysis runs. An unparsable value logs a warning and uses 30m." introduced:"v0.3.0"`

	SocialEnabled     bool          `env:"SKIPPER_SOCIAL_ENABLED" default:"false" desc:"Starts the social post drafting agent. Also needs SKIPPER_SOCIAL_NOTIFY_EMAIL and a chat or utility model." introduced:"v0.3.0"`
	SocialInterval    time.Duration `env:"SKIPPER_SOCIAL_INTERVAL" default:"2h" desc:"Interval between social post drafting runs." introduced:"v0.3.0"`
	SocialMaxPerDay   int           `env:"SKIPPER_SOCIAL_MAX_PER_DAY" default:"2" desc:"Maximum social post drafts sent per day." introduced:"v0.3.0"`
	SocialNotifyEmail string        `env:"SKIPPER_SOCIAL_NOTIFY_EMAIL" desc:"Address that receives social post drafts. Empty disables the social agent." introduced:"v0.3.0"`

	FromEmail       string `env:"FROM_EMAIL" default:"noreply@frameworks.network" desc:"Sender address for notification, infrastructure, and social emails." introduced:"v0.3.0"`
	FromName        string `env:"FROM_NAME" default:"FrameWorks" desc:"Sender display name for outgoing emails." introduced:"v0.3.0"`
	SMTPHost        string `env:"SMTP_HOST" desc:"SMTP server host for outgoing emails." introduced:"v0.3.0"`
	SMTPPort        string `env:"SMTP_PORT" default:"587" desc:"SMTP server port for outgoing emails." introduced:"v0.3.0"`
	SMTPUser        string `env:"SMTP_USER" desc:"SMTP user for outgoing emails." introduced:"v0.3.0"`
	SMTPPassword    string `env:"SMTP_PASSWORD" secret:"true" desc:"Password for SMTP_USER." introduced:"v0.3.0"`
	ToEmail         string `env:"TO_EMAIL" desc:"Fallback recipient for heartbeat reports and infrastructure alerts when no tenant contact resolves." introduced:"v0.3.0"`
	NotifyEmail     bool   `env:"SKIPPER_NOTIFY_EMAIL" default:"false" desc:"Default for delivering heartbeat report notifications by email when a tenant has no preference." introduced:"v0.3.0"`
	NotifyWebsocket bool   `env:"SKIPPER_NOTIFY_WEBSOCKET" default:"true" desc:"Default for delivering heartbeat report notifications over websocket when a tenant has no preference." introduced:"v0.3.0"`
	NotifyMCP       bool   `env:"SKIPPER_NOTIFY_MCP" default:"true" desc:"Default for delivering heartbeat report notifications over MCP when a tenant has no preference." introduced:"v0.3.0"`

	SMTPAllowInsecure bool `env:"SMTP_ALLOW_INSECURE" default:"false" desc:"Sends outgoing emails to an SMTP server that does not offer STARTTLS. For an isolated development or test relay only; never set it in production." introduced:"v0.3.10"`
}

// Validate rejects an origin rewrite map that does not parse.
func (c *Skipper) Validate() error {
	if _, err := skipperconfig.ParseOriginRewrites(c.CrawlOriginRewrites); err != nil {
		return fmt.Errorf("SKIPPER_CRAWL_ORIGIN_REWRITES %w", err)
	}
	return nil
}

// HeartbeatDuration parses HEARTBEAT_INTERVAL.
func (c *Skipper) HeartbeatDuration() (time.Duration, error) {
	return time.ParseDuration(c.HeartbeatInterval)
}

// Consultant resolves the runtime configuration: provider settings fall back
// to their LLM_* counterparts and the structured values are parsed.
func (c *Skipper) Consultant() (skipperconfig.Config, error) {
	rewrites, err := skipperconfig.ParseOriginRewrites(c.CrawlOriginRewrites)
	if err != nil {
		return skipperconfig.Config{}, fmt.Errorf("SKIPPER_CRAWL_ORIGIN_REWRITES %w", err)
	}
	return skipperconfig.Config{
		DatabaseURL:         c.DatabaseURL,
		LLMProvider:         c.LLMProvider,
		LLMModel:            c.LLMModel,
		LLMAPIKey:           c.LLMAPIKey,
		LLMAPIURL:           c.LLMAPIURL,
		LLMMaxTokens:        c.LLMMaxTokens,
		LLMContextWindow:    c.LLMContextWindow,
		PromptTokenBudget:   c.PromptTokenBudget,
		EmbeddingProvider:   orDefault(c.EmbeddingProvider, c.LLMProvider),
		EmbeddingModel:      orDefault(c.EmbeddingModel, c.LLMModel),
		EmbeddingAPIKey:     orDefault(c.EmbeddingAPIKey, c.LLMAPIKey),
		EmbeddingAPIURL:     orDefault(c.EmbeddingAPIURL, c.LLMAPIURL),
		EmbeddingDimensions: c.EmbeddingDimensions,
		SearchProvider:      c.SearchProvider,
		SearchAPIKey:        c.SearchAPIKey,
		SearchAPIURL:        c.SearchAPIURL,
		RequiredTierLevel:   c.RequiredTierLevel,
		ChatRateLimitHour:   c.ChatRateLimitPerHour,
		RateLimitOverrides:  skipperconfig.ParseRateLimitOverrides(c.ChatRateLimitOverrides),
		GatewayPublicURL:    c.GatewayPublicURL,
		GatewayMCPURL:       c.GatewayMCPURL,
		GatewayMCPURLs:      c.GatewayMCPURLs,
		AdminTenantID:       c.AdminTenantID,
		Sitemaps:            c.Sitemaps,
		SitemapsDir:         c.SitemapsDir,
		CrawlInterval:       c.CrawlInterval,
		SearchLimit:         c.SearchLimit,
		MaxHistoryMessages:  c.MaxHistoryMessages,
		ChunkTokenLimit:     c.ChunkTokenLimit,
		ChunkTokenOverlap:   c.ChunkTokenOverlap,
		EnableRendering:     c.EnableRendering,
		UtilityLLMProvider:  orDefault(c.UtilityLLMProvider, c.LLMProvider),
		UtilityLLMModel:     orDefault(c.UtilityLLMModel, c.LLMModel),
		UtilityLLMAPIKey:    orDefault(c.UtilityLLMAPIKey, c.LLMAPIKey),
		UtilityLLMAPIURL:    orDefault(c.UtilityLLMAPIURL, c.LLMAPIURL),
		ContextualRetrieval: c.ContextualRetrieval,
		LinkDiscovery:       c.LinkDiscovery,
		AdminAPIKey:         c.AdminAPIKey,
		RerankProvider:      c.RerankerProvider,
		RerankModel:         c.RerankerModel,
		RerankAPIKey:        orDefault(c.RerankerAPIKey, c.LLMAPIKey),
		RerankAPIURL:        c.RerankerAPIURL,
		EnableHyDE:          c.EnableHyDE,
		SSRFAllowedHosts:    c.SSRFAllowedHosts,
		CrawlOriginRewrites: rewrites,
		SocialEnabled:       c.SocialEnabled,
		SocialInterval:      c.SocialInterval,
		SocialMaxPerDay:     c.SocialMaxPerDay,
		SocialNotifyEmail:   c.SocialNotifyEmail,
	}, nil
}

func orDefault(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
