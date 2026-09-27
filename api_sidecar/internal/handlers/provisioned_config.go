package handlers

import (
	"os"
	"path/filepath"
	"strings"

	"frameworks/api_sidecar/internal/appconfig"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// ProvisionedConfigFile is the marker the edge provisioner writes into
// HELMSMAN_STATE_DIR after it renders the node's config. The frameworks.infra
// edge role and `frameworks edge doctor` use the same name.
const ProvisionedConfigFile = "provisioned-config.env"

// readProvisionedConfig returns the edge config marker in stateDir, or nil
// when the marker is absent, unreadable, or names neither field. The file is
// read on every lifecycle report so a re-provision shows up without a
// Helmsman restart.
func readProvisionedConfig(stateDir string) *ipcpb.EdgeProvisionedConfig {
	if strings.TrimSpace(stateDir) == "" {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(stateDir, ProvisionedConfigFile))
	if err != nil {
		return nil
	}
	cfg := &ipcpb.EdgeProvisionedConfig{}
	for line := range strings.SplitSeq(string(raw), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "EDGE_CONFIG_CLI_VERSION":
			cfg.CliVersion = strings.TrimSpace(value)
		case "EDGE_CONFIG_DIGEST":
			cfg.Digest = strings.TrimSpace(value)
		}
	}
	if cfg.GetCliVersion() == "" && cfg.GetDigest() == "" {
		return nil
	}
	return cfg
}

func currentProvisionedConfig() *ipcpb.EdgeProvisionedConfig {
	return readProvisionedConfig(appconfig.StateDir())
}
