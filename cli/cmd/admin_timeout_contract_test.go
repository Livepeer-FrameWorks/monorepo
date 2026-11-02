package cmd

import (
	"testing"
	"time"

	fwcfg "frameworks/cli/internal/config"
	"frameworks/cli/internal/controlplane"
)

func TestAdminClientTimeoutsCoverOperationDeadlines(t *testing.T) {
	ep := controlplane.Endpoint{Address: "control.example:443", ServerName: "control.example"}
	ctxCfg := fwcfg.Context{Auth: fwcfg.Auth{ServiceToken: "service-token"}}
	purserConfig := adminPurserGRPCConfig(ep, ctxCfg)
	quartermasterConfig := adminQuartermasterGRPCConfig(ep, ctxCfg)
	foghornConfig := adminFoghornGRPCConfig(ep, ctxCfg)

	if purserConfig.Timeout < cryptoSweepLongRPCTimeout {
		t.Fatalf("Purser client timeout %s truncates crypto sweep deadline %s", purserConfig.Timeout, cryptoSweepLongRPCTimeout)
	}
	if quartermasterConfig.Timeout < enableSelfHostingRPCTimeout {
		t.Fatalf("Quartermaster client timeout %s truncates self-hosting deadline %s", quartermasterConfig.Timeout, enableSelfHostingRPCTimeout)
	}
	if foghornConfig.Timeout < artifactMigrationRPCTimeout {
		t.Fatalf("Foghorn client timeout %s truncates artifact migration deadline %s", foghornConfig.Timeout, artifactMigrationRPCTimeout)
	}
	if quartermasterRPCTimeout < 30*time.Second {
		t.Fatalf("provisioning Quartermaster timeout %s is too short for a fresh port-forward", quartermasterRPCTimeout)
	}
	if quartermasterRPCTimeout >= provisionInitializeStallWindow {
		t.Fatalf("provisioning Quartermaster timeout %s must fit inside one initialize stall window %s", quartermasterRPCTimeout, provisionInitializeStallWindow)
	}
	if clusterNodesQuartermasterClientTimeout < edgeReleaseSyncRPCTimeout {
		t.Fatalf("cluster-nodes Quartermaster timeout %s truncates edge release deadline %s", clusterNodesQuartermasterClientTimeout, edgeReleaseSyncRPCTimeout)
	}
	if edgePreRegisterClientTimeout < 45*time.Second {
		t.Fatalf("edge preregistration client timeout %s truncates its 45s operation window", edgePreRegisterClientTimeout)
	}
	if edgeExternalIPDiscoveryTimeout >= edgePreRegisterClientTimeout {
		t.Fatalf("edge external-IP discovery timeout %s must leave an independent preregistration window of %s", edgeExternalIPDiscoveryTimeout, edgePreRegisterClientTimeout)
	}
	foghornNodeConfig := clusterNodesFoghornGRPCConfig(ep, ctxCfg)
	if foghornNodeConfig.Timeout != clusterNodesFoghornClientTimeout {
		t.Fatalf("cluster-nodes Foghorn timeout = %s, want %s", foghornNodeConfig.Timeout, clusterNodesFoghornClientTimeout)
	}
	retried := time.Duration(edgeReleaseSyncAttempts) * edgeReleaseSyncRPCTimeout
	for attempt := 0; attempt < edgeReleaseSyncAttempts-1; attempt++ {
		retried += edgeReleaseSyncBaseDelay << attempt
	}
	if edgeReleaseSyncStallWindow <= retried {
		t.Fatalf("edge release stall window %s must cover one cluster's fully retried target RPC (%s)", edgeReleaseSyncStallWindow, retried)
	}
}
