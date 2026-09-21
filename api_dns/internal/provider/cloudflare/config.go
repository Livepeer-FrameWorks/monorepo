package cloudflare

// Config holds CloudFlare API configuration
type Config struct {
	APIToken  string
	ZoneID    string
	AccountID string
}

// NewClientFromConfig creates a CloudFlare client from loaded configuration
func NewClientFromConfig(cfg *Config) *Client {
	return NewClient(cfg.APIToken, cfg.ZoneID, cfg.AccountID)
}
