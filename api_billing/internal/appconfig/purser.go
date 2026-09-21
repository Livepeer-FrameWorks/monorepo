// Package appconfig holds the typed configuration of the Purser binary and its
// subcommands. scripts/configref generates the operator configuration
// reference from these structs.
package appconfig

import (
	"reflect"
	"strings"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
)

// QuartermasterClient is the Quartermaster connection read by the server and
// the bootstrap subcommands.
type QuartermasterClient struct {
	QuartermasterGRPCAddr          string `env:"QUARTERMASTER_GRPC_ADDR" default:"quartermaster:19002" desc:"Quartermaster gRPC address for tenant, cluster access, and registration calls." introduced:"v0.3.0"`
	QuartermasterGRPCTLSServerName string `env:"QUARTERMASTER_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Quartermaster connection. Empty uses the canonical internal name." introduced:"v0.3.0"`
}

// Purser is the startup configuration of the Purser server. The embedded
// PurserRuntime is also served through Runtime, so the values Purser's
// internal packages read per request or per job tick follow a SIGHUP env-file
// reload; every other field applies at startup.
//
//configref:service purser cmd=cmd/purser
type Purser struct {
	config.HTTPListen
	config.GRPCListen
	config.HTTPRuntime
	config.Logging
	config.GRPCMetadataPolicy
	config.GeoIP
	config.ServiceAuth
	config.GRPCTLS
	config.Postgres
	config.Registration
	config.DomainEventActor
	QuartermasterClient
	PurserRuntime

	Region        string `env:"REGION" desc:"Region label attached to service events sent to Decklog." introduced:"v0.3.0"`
	AdvertiseHost string `env:"PURSER_HOST" default:"purser" desc:"Host name this instance advertises when it registers with Quartermaster." introduced:"v0.3.0"`

	ClusterAccessMaterializationSecret string `env:"CLUSTER_ACCESS_MATERIALIZATION_SECRET" required:"true" secret:"true" desc:"Shared with Quartermaster. Signs the commercial and owner grant materialization and revocation envelopes Purser sends to Quartermaster. Read at startup; a rotation takes effect when Purser and Quartermaster restart." introduced:"v0.3.0"`

	CommodoreGRPCAddr          string `env:"COMMODORE_GRPC_ADDR" default:"commodore:19001" desc:"Commodore gRPC address for stream termination and cache invalidation when balances or suspensions change." introduced:"v0.3.0"`
	PeriscopeGRPCAddr          string `env:"PERISCOPE_GRPC_ADDR" default:"periscope-query:19004" desc:"Periscope Query gRPC address for invoice usage enrichment such as unique viewer counts and geographic breakdowns." introduced:"v0.3.0"`
	DecklogGRPCAddr            string `env:"DECKLOG_GRPC_ADDR" default:"decklog:18006" desc:"Decklog gRPC address for service events." introduced:"v0.3.0"`
	CommodoreGRPCTLSServerName string `env:"COMMODORE_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Commodore connection. Empty uses the canonical internal name." introduced:"v0.3.0"`
	PeriscopeGRPCTLSServerName string `env:"PERISCOPE_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Periscope Query connection. Empty uses the canonical internal name." introduced:"v0.3.0"`
	DecklogGRPCTLSServerName   string `env:"DECKLOG_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Decklog connection. Empty uses the canonical internal name." introduced:"v0.3.0"`

	LivepeerDepositMonitorEnabled bool `env:"LIVEPEER_DEPOSIT_MONITOR_ENABLED" default:"false" desc:"Starts the Livepeer TicketBroker deposit monitor, which funds low gateway deposits on Arbitrum from the X402_GAS_WALLET_PRIVKEY wallet. When true, an invalid wallet key or ARBITRUM_RPC_ENDPOINT stops startup." introduced:"v0.3.0"`
}

// PurserRuntime holds every value Purser's internal packages read. They read
// it through Runtime: values read per request or per job tick follow a SIGHUP
// env-file reload, while values read by constructors apply at startup.
type PurserRuntime struct {
	config.BuildEnvironment
	config.EmailBranding
	CryptoNetworks

	CryptoDepositsEnabled bool `env:"CRYPTO_DEPOSITS_ENABLED" default:"true" desc:"Emergency breaker for new direct crypto invoice payments and prepaid top-ups. Existing deposits keep reconciling while it is false. Re-read after an env-file reload." introduced:"v0.3.0"`
	X402PaymentsEnabled   bool `env:"X402_PAYMENTS_ENABLED" default:"true" desc:"Emergency breaker for advertising, verifying, and settling new x402 payments. Submitted settlements keep reconciling while it is false. Re-read after an env-file reload." introduced:"v0.3.0"`
	WaiveUsageCharges     bool `env:"WAIVE_USAGE_CHARGES" default:"false" desc:"Rates metered usage at zero while subscription base fees still charge, and skips the metering completeness requirement for invoices. Re-read after an env-file reload." introduced:"v0.3.0"`
	X402IncludeTestnets   bool `env:"X402_INCLUDE_TESTNETS" default:"false" desc:"Includes Base Sepolia and Arbitrum Sepolia in x402 payments, crypto deposits, and sweeps. Testnet x402 payments stay refused in production. Sweeps and readiness checks re-read it after an env-file reload; the x402 handler and monitors read it at startup." introduced:"v0.3.0"`

	GatewayPublicURL    string `env:"GATEWAY_PUBLIC_URL" desc:"Public Bridge URL. Mollie webhooks target its /webhooks/billing/mollie path and x402 resource URLs are built from it; Mollie card payments and production x402 require it. Re-read after an env-file reload." introduced:"v0.3.0"`
	PaymentCardProvider string `env:"PAYMENT_CARD_PROVIDER" desc:"Card provider for invoice payments, stripe or mollie. Empty selects the one fully configured provider and is rejected when both are configured. A card provider is offered only when WEBAPP_PUBLIC_URL is set. Re-read after an env-file reload." introduced:"v0.3.0"`
	StripeSecretKey     string `env:"STRIPE_SECRET_KEY" secret:"true" desc:"Stripe API secret key. Enables the Stripe client and billing tier price sync at startup; checkout and provider availability re-read it after an env-file reload." introduced:"v0.3.0"`
	StripeWebhookSecret string `env:"STRIPE_WEBHOOK_SECRET" secret:"true" desc:"Signing secret that verifies Stripe webhooks. Empty rejects every Stripe webhook. Re-read after an env-file reload." introduced:"v0.3.0"`
	MollieAPIKey        string `env:"MOLLIE_API_KEY" secret:"true" desc:"Mollie API key. Enables the Mollie client at startup; checkout and provider availability re-read it after an env-file reload." introduced:"v0.3.0"`

	SupplierName               string `env:"SUPPLIER_NAME" desc:"Supplier legal name on billing documents and crypto invoices. The complete SUPPLIER_* set is required for x402 and, in production, for crypto deposits." introduced:"v0.3.0"`
	SupplierAddress            string `env:"SUPPLIER_ADDRESS" desc:"Supplier postal address on billing documents and crypto invoices." introduced:"v0.3.0"`
	SupplierVATNumber          string `env:"SUPPLIER_VAT_NUMBER" desc:"Supplier VAT number on billing documents and crypto invoices." introduced:"v0.3.0"`
	SupplierRegistrationNumber string `env:"SUPPLIER_REGISTRATION_NUMBER" desc:"Supplier company registration number on billing documents and crypto invoices." introduced:"v0.3.0"`
	SupplierCountry            string `env:"SUPPLIER_COUNTRY" desc:"Supplier country, normalized to a two-letter country code, for crypto invoices and crypto deposit readiness." introduced:"v0.3.0"`
	VIESEndpoint               string `env:"VIES_ENDPOINT" desc:"EU VIES SOAP endpoint that validates customer VAT numbers. Empty uses the European Commission service. Re-read after an env-file reload." introduced:"v0.3.0"`

	HDWalletXpub         string `env:"HD_WALLET_XPUB" secret:"true" desc:"Extended public key that initializes HD wallet deposit address state at startup when the database has none. A different stored key is kept and the mismatch is logged." introduced:"v0.3.0"`
	X402GasWalletPrivkey string `env:"X402_GAS_WALLET_PRIVKEY" secret:"true" desc:"Hex private key of the gas wallet that submits self-facilitated x402 settlements and funds Livepeer TicketBroker deposits." introduced:"v0.3.0"`
	X402GasWalletAddress string `env:"X402_GAS_WALLET_ADDRESS" desc:"Gas wallet address. Empty derives it from X402_GAS_WALLET_PRIVKEY; a mismatch blocks self-facilitated x402 and the Livepeer deposit monitor." introduced:"v0.3.0"`

	X402TopupUSDCents         int    `env:"X402_TOPUP_USD_CENTS" default:"500" desc:"Minimum amount in US cents an x402 quote asks for; a larger prepaid deficit plus buffer raises it. A value outside 1 to 10000 logs a warning and uses 500." introduced:"v0.3.0"`
	X402PrepaidBufferEurCents int    `env:"X402_PREPAID_BUFFER_EUR_CENTS" default:"500" desc:"Euro cents of prepaid credit an x402 quote adds on top of any negative balance. Zero or negative uses 500. Re-read after an env-file reload." introduced:"v0.3.0"`
	X402FacilitatorProvider   string `env:"X402_FACILITATOR_PROVIDER" desc:"x402 facilitator. self settles through the gas wallet, cdp uses the Coinbase CDP facilitator, and hosted uses X402_FACILITATOR_URL. Empty means self." introduced:"v0.3.0"`
	X402FacilitatorURL        string `env:"X402_FACILITATOR_URL" desc:"HTTPS base URL of the cdp or hosted x402 facilitator. Empty uses the Coinbase CDP URL for cdp." introduced:"v0.3.0"`
	CDPAPIKeyID               string `env:"CDP_API_KEY_ID" desc:"Coinbase CDP API key ID for the cdp x402 facilitator." introduced:"v0.3.0"`
	CDPAPIKeySecret           string `env:"CDP_API_KEY_SECRET" secret:"true" desc:"Coinbase CDP API key secret for the cdp x402 facilitator. Literal backslash-n sequences are converted to newlines." introduced:"v0.3.0"`
	X402RecoveryWindowHours   int    `env:"X402_RECOVERY_WINDOW_HOURS" default:"168" desc:"Hours back the x402 reconciler looks for failed settlements to recover. Zero or negative uses 168." introduced:"v0.3.0"`
	X402ReorgDepthBlocks      int    `env:"X402_REORG_DEPTH_BLOCKS" default:"50" desc:"Blocks the x402 reconciler waits past a settlement block before treating a missing receipt as reorganized. Zero or negative uses 50." introduced:"v0.3.0"`
	X402RPCErrorLimit         int    `env:"X402_RPC_ERROR_LIMIT" default:"5" desc:"Consecutive RPC errors per network after which the x402 reconciler logs a warning and emits a billing event. Zero or negative uses 5." introduced:"v0.3.0"`

	LivepeerDepositLowThreshold string `env:"LIVEPEER_DEPOSIT_LOW_THRESHOLD" default:"0.1" desc:"ETH TicketBroker deposit and reserve level below which the Livepeer deposit monitor funds a gateway. An unparsable value logs a warning and uses 0.1." introduced:"v0.3.0"`
	LivepeerTopupAmount         string `env:"LIVEPEER_TOPUP_AMOUNT" default:"0.2" desc:"ETH amount the Livepeer deposit monitor sends per TicketBroker funding. An unparsable value logs a warning and uses 0.2." introduced:"v0.3.0"`
	LivepeerFundingDailyCap     string `env:"LIVEPEER_FUNDING_DAILY_CAP" default:"1" desc:"Maximum ETH the Livepeer deposit monitor spends on TicketBroker funding per day. An unparsable or non-positive value logs a warning and uses 1." introduced:"v0.3.0"`

	CryptoSweepETHDustWei string `env:"CRYPTO_SWEEP_ETH_DUST_WEI" default:"100000000000000" desc:"Wei left in each ETH source address when a custody sweep is planned. An unparsable or negative value uses 100000000000000. Re-read after an env-file reload." introduced:"v0.3.0"`

	SMTPHost     string `env:"SMTP_HOST" desc:"SMTP server for billing notification emails. Emails are sent only when SMTP_HOST, SMTP_USER, SMTP_PASSWORD, and FROM_EMAIL are set." introduced:"v0.3.0"`
	SMTPPort     int    `env:"SMTP_PORT" default:"587" desc:"SMTP server port for billing notification emails. 0 uses 587." introduced:"v0.3.0"`
	SMTPUser     string `env:"SMTP_USER" desc:"SMTP user for billing notification emails." introduced:"v0.3.0"`
	SMTPPassword string `env:"SMTP_PASSWORD" secret:"true" desc:"Password for SMTP_USER." introduced:"v0.3.0"`
	FromEmail    string `env:"FROM_EMAIL" desc:"Sender address of billing notification emails." introduced:"v0.3.0"`
	FromName     string `env:"FROM_NAME" desc:"Sender display name of billing notification emails. Empty uses FrameWorks." introduced:"v0.3.0"`

	SMTPAllowInsecure bool `env:"SMTP_ALLOW_INSECURE" default:"false" desc:"Sends billing notification emails to an SMTP server that does not offer STARTTLS. For an isolated development or test relay only; never set it in production." introduced:"v0.3.10"`

	KafkaBrokers      []string `env:"KAFKA_BROKERS" default:"kafka:9092" desc:"Comma-separated Kafka bootstrap brokers for the billing usage report consumer." introduced:"v0.3.0"`
	KafkaClusterID    string   `env:"KAFKA_CLUSTER_ID" default:"local" desc:"Kafka cluster identifier passed to the billing usage report consumer." introduced:"v0.3.0"`
	KafkaClientID     string   `env:"KAFKA_CLIENT_ID" default:"purser" desc:"Kafka client ID of the billing usage report consumer." introduced:"v0.3.0"`
	KafkaGroupID      string   `env:"KAFKA_GROUP_ID" default:"purser-ingest" desc:"Kafka consumer group of the billing usage report consumer." introduced:"v0.3.0"`
	BillingKafkaTopic string   `env:"BILLING_KAFKA_TOPIC" default:"billing.usage_reports" desc:"Kafka topic the billing usage report consumer reads." introduced:"v0.3.0"`
}

// IsProduction reports whether BUILD_ENV selects production runtime behavior.
func (r *PurserRuntime) IsProduction() bool {
	switch strings.ToLower(strings.TrimSpace(r.BuildEnv)) {
	case "production", "prod":
		return true
	default:
		return false
	}
}

// GatewayPublicBaseURL is GATEWAY_PUBLIC_URL without trailing slashes.
func (r *PurserRuntime) GatewayPublicBaseURL() string {
	return strings.TrimRight(r.GatewayPublicURL, "/")
}

// CryptoNetworks holds the per-network crypto settings. Callers derive the key
// from a network registry entry, so they read values by key through
// NetworkSetting. The suffix is the network name in upper snake case.
type CryptoNetworks struct {
	EthRPCEndpoint             string `env:"ETH_RPC_ENDPOINT" secret:"true" desc:"Ethereum mainnet JSON-RPC endpoint. Empty uses https://ethereum-rpc.publicnode.com. Re-read after an env-file reload." introduced:"v0.3.0"`
	BaseRPCEndpoint            string `env:"BASE_RPC_ENDPOINT" secret:"true" desc:"Base mainnet JSON-RPC endpoint. Empty uses https://base.publicnode.com. Re-read after an env-file reload." introduced:"v0.3.0"`
	ArbitrumRPCEndpoint        string `env:"ARBITRUM_RPC_ENDPOINT" secret:"true" desc:"Arbitrum One JSON-RPC endpoint. Empty uses https://arb1.arbitrum.io/rpc, except for the Livepeer deposit monitor, which requires it. Re-read after an env-file reload." introduced:"v0.3.0"`
	BaseSepoliaRPCEndpoint     string `env:"BASE_SEPOLIA_RPC_ENDPOINT" secret:"true" desc:"Base Sepolia JSON-RPC endpoint. Empty uses https://base-sepolia.publicnode.com. Re-read after an env-file reload." introduced:"v0.3.0"`
	ArbitrumSepoliaRPCEndpoint string `env:"ARBITRUM_SEPOLIA_RPC_ENDPOINT" secret:"true" desc:"Arbitrum Sepolia JSON-RPC endpoint. Empty uses https://sepolia-rollup.arbitrum.io/rpc. Re-read after an env-file reload." introduced:"v0.3.0"`
	EtherscanAPIKey            string `env:"ETHERSCAN_API_KEY" secret:"true" desc:"Etherscan API key for block explorer transaction lookups by the crypto payment monitor on every network. Re-read after an env-file reload." introduced:"v0.3.0"`

	CryptoTreasuryEthereum        string `env:"CRYPTO_TREASURY_ETHEREUM" desc:"Non-zero Ethereum mainnet treasury address that custody sweeps send funds to. Required to advertise deposits in production. Re-read after an env-file reload." introduced:"v0.3.0"`
	CryptoTreasuryBase            string `env:"CRYPTO_TREASURY_BASE" desc:"Non-zero Base mainnet treasury address that custody sweeps send funds to. Required to advertise deposits and x402 in production. Re-read after an env-file reload." introduced:"v0.3.0"`
	CryptoTreasuryArbitrum        string `env:"CRYPTO_TREASURY_ARBITRUM" desc:"Non-zero Arbitrum One treasury address that custody sweeps send funds to. Required to advertise deposits and x402 in production. Re-read after an env-file reload." introduced:"v0.3.0"`
	CryptoTreasuryBaseSepolia     string `env:"CRYPTO_TREASURY_BASE_SEPOLIA" desc:"Non-zero Base Sepolia treasury address that custody sweeps send funds to. Re-read after an env-file reload." introduced:"v0.3.0"`
	CryptoTreasuryArbitrumSepolia string `env:"CRYPTO_TREASURY_ARBITRUM_SEPOLIA" desc:"Non-zero Arbitrum Sepolia treasury address that custody sweeps send funds to. Re-read after an env-file reload." introduced:"v0.3.0"`

	CryptoSweepRelayerPrivateKeyEthereum        string `env:"CRYPTO_SWEEP_RELAYER_PRIVATE_KEY_ETHEREUM" secret:"true" desc:"Hex private key of the dedicated gas relayer that submits USDC custody sweeps on Ethereum mainnet. Required to advertise deposits in production. Re-read after an env-file reload." introduced:"v0.3.0"`
	CryptoSweepRelayerPrivateKeyBase            string `env:"CRYPTO_SWEEP_RELAYER_PRIVATE_KEY_BASE" secret:"true" desc:"Hex private key of the dedicated gas relayer that submits USDC custody sweeps on Base mainnet. Required to advertise deposits and x402 in production. Re-read after an env-file reload." introduced:"v0.3.0"`
	CryptoSweepRelayerPrivateKeyArbitrum        string `env:"CRYPTO_SWEEP_RELAYER_PRIVATE_KEY_ARBITRUM" secret:"true" desc:"Hex private key of the dedicated gas relayer that submits USDC custody sweeps on Arbitrum One. Required to advertise deposits and x402 in production. Re-read after an env-file reload." introduced:"v0.3.0"`
	CryptoSweepRelayerPrivateKeyBaseSepolia     string `env:"CRYPTO_SWEEP_RELAYER_PRIVATE_KEY_BASE_SEPOLIA" secret:"true" desc:"Hex private key of the dedicated gas relayer that submits USDC custody sweeps on Base Sepolia. Re-read after an env-file reload." introduced:"v0.3.0"`
	CryptoSweepRelayerPrivateKeyArbitrumSepolia string `env:"CRYPTO_SWEEP_RELAYER_PRIVATE_KEY_ARBITRUM_SEPOLIA" secret:"true" desc:"Hex private key of the dedicated gas relayer that submits USDC custody sweeps on Arbitrum Sepolia. Re-read after an env-file reload." introduced:"v0.3.0"`

	CryptoScanStartBlockEthereum        string `env:"CRYPTO_SCAN_START_BLOCK_ETHEREUM" desc:"Ethereum mainnet block where the crypto deposit scanner starts when it has no cursor. Required in production; otherwise the scan starts 1000 blocks behind the safe head." introduced:"v0.3.0"`
	CryptoScanStartBlockBase            string `env:"CRYPTO_SCAN_START_BLOCK_BASE" desc:"Base mainnet block where the crypto deposit scanner starts when it has no cursor. Required in production; otherwise the scan starts 1000 blocks behind the safe head." introduced:"v0.3.0"`
	CryptoScanStartBlockArbitrum        string `env:"CRYPTO_SCAN_START_BLOCK_ARBITRUM" desc:"Arbitrum One block where the crypto deposit scanner starts when it has no cursor. Required in production; otherwise the scan starts 1000 blocks behind the safe head." introduced:"v0.3.0"`
	CryptoScanStartBlockBaseSepolia     string `env:"CRYPTO_SCAN_START_BLOCK_BASE_SEPOLIA" desc:"Base Sepolia block where the crypto deposit scanner starts when it has no cursor. Required in production; otherwise the scan starts 1000 blocks behind the safe head." introduced:"v0.3.0"`
	CryptoScanStartBlockArbitrumSepolia string `env:"CRYPTO_SCAN_START_BLOCK_ARBITRUM_SEPOLIA" desc:"Arbitrum Sepolia block where the crypto deposit scanner starts when it has no cursor. Required in production; otherwise the scan starts 1000 blocks behind the safe head." introduced:"v0.3.0"`
}

var cryptoNetworkFieldByKey = func() map[string]int {
	t := reflect.TypeFor[CryptoNetworks]()
	index := make(map[string]int, t.NumField())
	for i := range t.NumField() {
		if key := t.Field(i).Tag.Get(config.TagEnv); key != "" {
			index[key] = i
		}
	}
	return index
}()

// NetworkSetting returns the per-network value declared under key, or an empty
// string when key is not a declared per-network setting.
func (n *CryptoNetworks) NetworkSetting(key string) string {
	i, ok := cryptoNetworkFieldByKey[key]
	if !ok {
		return ""
	}
	return reflect.ValueOf(n).Elem().Field(i).String()
}

// PurserBootstrap is the configuration of `purser bootstrap`. SERVICE_TOKEN is
// optional here because only a desired state with customer_billing entries
// dials Quartermaster; the subcommand checks it when it needs it.
//
//configref:service purser cmd=cmd/purser variant=bootstrap
type PurserBootstrap struct {
	config.Postgres
	QuartermasterClient
	config.GRPCClientTLS

	ServiceToken string `env:"SERVICE_TOKEN" secret:"true" desc:"Shared service token for Quartermaster calls. Required when the desired state declares customer_billing." introduced:"v0.3.0"`
}

// PurserDataMigrations is the configuration of `purser data-migrations`.
//
//configref:service purser cmd=cmd/purser variant=data-migrations
type PurserDataMigrations struct {
	config.Postgres
}

// PurserBootstrapValidate is the configuration of `purser bootstrap validate`.
//
//configref:service purser cmd=cmd/purser variant=bootstrap-validate
type PurserBootstrapValidate struct {
	config.Postgres
	QuartermasterClient
	config.GRPCClientTLS

	ServiceToken string `env:"SERVICE_TOKEN" required:"true" secret:"true" desc:"Shared service token for listing clusters through Quartermaster." introduced:"v0.3.0"`
}
