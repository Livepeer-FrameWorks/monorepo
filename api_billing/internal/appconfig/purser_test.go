package appconfig_test

import (
	"errors"
	"slices"
	"testing"

	"frameworks/api_billing/internal/appconfig"
	"frameworks/api_billing/internal/appconfig/appconfigtest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
)

func lookupFrom(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := values[key]
		return v, ok
	}
}

var purserRequired = map[string]string{
	"DATABASE_URL":                          "postgres://purser",
	"JWT_SECRET":                            "jwt",
	"SERVICE_TOKEN":                         "token",
	"CLUSTER_ACCESS_MATERIALIZATION_SECRET": "shared",
	"USAGE_HASH_SECRET":                     "usage-hash",
}

func TestPurserRequiresDatabaseAndSharedSecrets(t *testing.T) {
	_, err := config.Load[appconfig.Purser](config.Options{Service: "purser", Lookup: lookupFrom(nil)})
	var loadErr *config.LoadError
	if !errors.As(err, &loadErr) {
		t.Fatalf("Load error = %v, want LoadError", err)
	}
	for key := range purserRequired {
		if !slices.Contains(loadErr.Missing, key) {
			t.Errorf("missing keys %v do not name %s", loadErr.Missing, key)
		}
	}
}

func TestPurserStartupDefaults(t *testing.T) {
	cfg, err := config.Load[appconfig.Purser](config.Options{Service: "purser", Lookup: lookupFrom(purserRequired)})
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string][2]string{
		"PORT":                    {cfg.HTTPListen.Port, "18003"},
		"GRPC_PORT":               {cfg.GRPCListen.Port, "19003"},
		"QUARTERMASTER_GRPC_ADDR": {cfg.QuartermasterGRPCAddr, "quartermaster:19002"},
		"COMMODORE_GRPC_ADDR":     {cfg.CommodoreGRPCAddr, "commodore:19001"},
		"PERISCOPE_GRPC_ADDR":     {cfg.PeriscopeGRPCAddr, "periscope-query:19004"},
		"DECKLOG_GRPC_ADDR":       {cfg.DecklogGRPCAddr, "decklog:18006"},
		"PURSER_HOST":             {cfg.AdvertiseHost, "purser"},
		"GIN_MODE":                {cfg.GinMode, "debug"},
	} {
		if got[0] != got[1] {
			t.Errorf("%s = %q, want %q", name, got[0], got[1])
		}
	}
	if cfg.LivepeerDepositMonitorEnabled || cfg.AllowInsecure {
		t.Errorf("LIVEPEER_DEPOSIT_MONITOR_ENABLED=%v GRPC_ALLOW_INSECURE=%v, want both false", cfg.LivepeerDepositMonitorEnabled, cfg.AllowInsecure)
	}
}

func TestPurserRuntimeDefaultsKeepBreakerPositions(t *testing.T) {
	rt, err := config.Load[appconfig.PurserRuntime](config.Options{Service: "purser", Lookup: lookupFrom(nil)})
	if err != nil {
		t.Fatal(err)
	}
	if !rt.CryptoDepositsEnabled || !rt.X402PaymentsEnabled {
		t.Errorf("CRYPTO_DEPOSITS_ENABLED=%v X402_PAYMENTS_ENABLED=%v, want default-on", rt.CryptoDepositsEnabled, rt.X402PaymentsEnabled)
	}
	if rt.WaiveUsageCharges || rt.X402IncludeTestnets {
		t.Errorf("WAIVE_USAGE_CHARGES=%v X402_INCLUDE_TESTNETS=%v, want default-off", rt.WaiveUsageCharges, rt.X402IncludeTestnets)
	}
	for name, got := range map[string][2]int{
		"X402_TOPUP_USD_CENTS":          {rt.X402TopupUSDCents, 500},
		"X402_PREPAID_BUFFER_EUR_CENTS": {rt.X402PrepaidBufferEurCents, 500},
		"X402_RECOVERY_WINDOW_HOURS":    {rt.X402RecoveryWindowHours, 168},
		"X402_REORG_DEPTH_BLOCKS":       {rt.X402ReorgDepthBlocks, 50},
		"X402_RPC_ERROR_LIMIT":          {rt.X402RPCErrorLimit, 5},
		"SMTP_PORT":                     {rt.SMTPPort, 587},
	} {
		if got[0] != got[1] {
			t.Errorf("%s = %d, want %d", name, got[0], got[1])
		}
	}
	if !slices.Equal(rt.KafkaBrokers, []string{"kafka:9092"}) || rt.KafkaGroupID != "purser-ingest" || rt.BillingKafkaTopic != "billing.usage_reports" {
		t.Errorf("kafka defaults = %v %q %q", rt.KafkaBrokers, rt.KafkaGroupID, rt.BillingKafkaTopic)
	}
	if got := appconfig.Runtime(); !got.CryptoDepositsEnabled || !got.X402PaymentsEnabled || got.WaiveUsageCharges {
		t.Errorf("Runtime without an installed source = %+v, want declared defaults", got)
	}
}

func TestPurserRuntimeEmailBranding(t *testing.T) {
	rt, err := config.Load[appconfig.PurserRuntime](config.Options{Service: "purser", Lookup: lookupFrom(map[string]string{
		"EMAIL_LOGO_URL":    "https://cdn.example.test/logo.png",
		"WEBAPP_PUBLIC_URL": "https://app.example.test",
		"SUPPORT_EMAIL":     "help@example.test",
	})})
	if err != nil {
		t.Fatal(err)
	}
	want := config.EmailBranding{LogoURL: "https://cdn.example.test/logo.png", WebAppURL: "https://app.example.test", SupportEmail: "help@example.test"}
	if rt.EmailBranding != want {
		t.Errorf("EmailBranding = %+v, want %+v", rt.EmailBranding, want)
	}
	if got := appconfig.Runtime().Support(); got != config.DefaultSupportEmail {
		t.Errorf("Runtime without an installed source Support() = %q, want %q", got, config.DefaultSupportEmail)
	}
}

func TestRuntimeFollowsReloadAndKeepsSnapshotOnInvalidValue(t *testing.T) {
	values := map[string]string{}
	opts := config.Options{Service: "purser", Lookup: lookupFrom(values)}
	initial, err := config.Load[appconfig.PurserRuntime](opts)
	if err != nil {
		t.Fatal(err)
	}
	live := config.NewLive(initial, opts)
	previous := appconfig.InstallRuntime(live)
	t.Cleanup(func() { appconfig.InstallRuntime(previous) })

	values["CRYPTO_DEPOSITS_ENABLED"] = "false"
	values["X402_PAYMENTS_ENABLED"] = "false"
	values["WAIVE_USAGE_CHARGES"] = "true"
	values["X402_INCLUDE_TESTNETS"] = "true"
	if !appconfig.Runtime().CryptoDepositsEnabled {
		t.Fatal("snapshot changed before a reload")
	}
	if err := live.Reload(); err != nil {
		t.Fatal(err)
	}
	rt := appconfig.Runtime()
	if rt.CryptoDepositsEnabled || rt.X402PaymentsEnabled || !rt.WaiveUsageCharges || !rt.X402IncludeTestnets {
		t.Fatalf("reloaded breakers = deposits %v x402 %v waive %v testnets %v", rt.CryptoDepositsEnabled, rt.X402PaymentsEnabled, rt.WaiveUsageCharges, rt.X402IncludeTestnets)
	}

	values["CRYPTO_DEPOSITS_ENABLED"] = "maybe"
	values["X402_PAYMENTS_ENABLED"] = "true"
	if err := live.Reload(); err == nil {
		t.Fatal("reload accepted an invalid breaker value")
	}
	if rt := appconfig.Runtime(); rt.CryptoDepositsEnabled || rt.X402PaymentsEnabled {
		t.Fatal("a rejected reload replaced the working snapshot")
	}
}

func TestNetworkSettingResolvesOnlyPerNetworkKeys(t *testing.T) {
	appconfigtest.Set(t, "CRYPTO_TREASURY_BASE_SEPOLIA", "0xabc")
	appconfigtest.Set(t, "BASE_RPC_ENDPOINT", " https://base.example ")
	appconfigtest.Set(t, "SUPPLIER_NAME", "FrameWorks")
	rt := appconfig.Runtime()
	if got := rt.NetworkSetting("CRYPTO_TREASURY_BASE_SEPOLIA"); got != "0xabc" {
		t.Errorf("treasury = %q", got)
	}
	if got := rt.NetworkSetting("BASE_RPC_ENDPOINT"); got != "https://base.example" {
		t.Errorf("rpc endpoint = %q", got)
	}
	if got := rt.NetworkSetting("SUPPLIER_NAME"); got != "" {
		t.Errorf("non-network key resolved to %q", got)
	}
	if got := rt.NetworkSetting("CRYPTO_TREASURY_UNKNOWN"); got != "" {
		t.Errorf("undeclared key resolved to %q", got)
	}
}

func TestAppconfigtestSetRestoresPreviousRuntime(t *testing.T) {
	t.Run("set", func(t *testing.T) {
		appconfigtest.Set(t, "X402_PAYMENTS_ENABLED", "false")
		appconfigtest.Set(t, "WAIVE_USAGE_CHARGES", "true")
		if rt := appconfig.Runtime(); rt.X402PaymentsEnabled || !rt.WaiveUsageCharges {
			t.Fatal("Set values are not visible through Runtime")
		}
	})
	if rt := appconfig.Runtime(); !rt.X402PaymentsEnabled || rt.WaiveUsageCharges {
		t.Fatal("Set was not restored when the subtest ended")
	}
}

func TestIsProductionAndGatewayBaseURL(t *testing.T) {
	for buildEnv, want := range map[string]bool{"production": true, " Prod ": true, "staging": false, "": false, "development": false} {
		rt, err := config.Load[appconfig.PurserRuntime](config.Options{Service: "purser", Lookup: lookupFrom(map[string]string{"BUILD_ENV": buildEnv})})
		if err != nil {
			t.Fatal(err)
		}
		if got := rt.IsProduction(); got != want {
			t.Errorf("IsProduction(BUILD_ENV=%q) = %v, want %v", buildEnv, got, want)
		}
	}
	rt := &appconfig.PurserRuntime{GatewayPublicURL: "https://gateway.example//"}
	if got := rt.GatewayPublicBaseURL(); got != "https://gateway.example" {
		t.Errorf("GatewayPublicBaseURL = %q", got)
	}
}

func TestBootstrapVariantsRequireOnlyWhatTheyUse(t *testing.T) {
	apply, err := config.Load[appconfig.PurserBootstrap](config.Options{Service: "purser", Lookup: lookupFrom(map[string]string{"DATABASE_URL": "postgres://purser"})})
	if err != nil {
		t.Fatalf("bootstrap apply without SERVICE_TOKEN: %v", err)
	}
	if apply.ServiceToken != "" || apply.QuartermasterGRPCAddr != "quartermaster:19002" {
		t.Errorf("bootstrap apply = %+v", apply)
	}

	_, err = config.Load[appconfig.PurserBootstrapValidate](config.Options{Service: "purser", Lookup: lookupFrom(map[string]string{"DATABASE_URL": "postgres://purser"})})
	var loadErr *config.LoadError
	if !errors.As(err, &loadErr) || !slices.Equal(loadErr.Missing, []string{"SERVICE_TOKEN"}) {
		t.Fatalf("bootstrap validate error = %v, want missing SERVICE_TOKEN", err)
	}
}
