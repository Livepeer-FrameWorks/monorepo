package handlers

// UsageDetails represents structured usage details for JSONB storage in invoices
type UsageDetails struct {
	UsageData    map[string]float64 `json:"usage_data"`
	BillingMonth string             `json:"billing_month"`
	TierInfo     TierInfo           `json:"tier_info"`
}

// TierInfo represents tier information within usage details
type TierInfo struct {
	TierID          string  `json:"tier_id"`
	TierName        string  `json:"tier_name"`
	DisplayName     string  `json:"display_name"`
	BasePrice       float64 `json:"base_price"`
	MeteringEnabled bool    `json:"metering_enabled"`
}

// MollieWebhookPayload represents a webhook payload from Mollie
type MollieWebhookPayload struct {
	ID       string `json:"id"`
	Resource string `json:"resource"`
	Status   string `json:"status"`
}

// BlockCypherTransactionInput represents a transaction input for BlockCypher API
type BlockCypherTransactionInput struct {
	Addresses []string `json:"addresses"`
}

// BlockCypherTransactionOutput represents a transaction output for BlockCypher API
type BlockCypherTransactionOutput struct {
	Addresses []string `json:"addresses"`
	Value     int64    `json:"value"`
}

// BlockCypherTransactionRequest represents a transaction request to BlockCypher API
type BlockCypherTransactionRequest struct {
	Inputs      []BlockCypherTransactionInput  `json:"inputs"`
	Outputs     []BlockCypherTransactionOutput `json:"outputs"`
	PrivateKeys []string                       `json:"private_keys"`
}
