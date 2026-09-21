package handlers

import (
	"testing"

	"frameworks/api_billing/internal/appconfig"
	"frameworks/api_billing/internal/appconfig/appconfigtest"
)

// Every key a registry network derives must be a declared per-network runtime
// setting; an undeclared key would silently resolve to an empty value.
func TestNetworkRegistryKeysAreDeclaredRuntimeSettings(t *testing.T) {
	for name, network := range Networks {
		keys := []string{
			network.RPCEndpointEnv,
			network.ExplorerAPIEnv,
			cryptoNetworkEnvKey("CRYPTO_TREASURY", name),
			cryptoNetworkEnvKey("CRYPTO_SWEEP_RELAYER_PRIVATE_KEY", name),
			cryptoNetworkEnvKey("CRYPTO_SCAN_START_BLOCK", name),
		}
		for _, key := range keys {
			appconfigtest.Set(t, key, "value-of-"+key)
			if got := appconfig.Runtime().NetworkSetting(key); got != "value-of-"+key {
				t.Errorf("network %s: NetworkSetting(%s) = %q", name, key, got)
			}
		}
	}
}
