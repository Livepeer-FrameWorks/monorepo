package main

import (
	"testing"
	"time"

	"frameworks/api_tenants/internal/appconfig"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func TestQuartermasterPurserClientUsesBoundedPureServiceAuthentication(t *testing.T) {
	logger := logging.NewLoggerWithService("quartermaster-test")
	cfg := &appconfig.Quartermaster{PurserGRPCAddr: "purser.test:19003", PurserGRPCTLSServerName: "purser.internal"}
	cfg.ServiceToken = "service-secret"
	cfg.CAPath = "/etc/frameworks/pki/ca.crt"

	clientCfg := quartermasterPurserClientConfig(cfg, logger)
	if clientCfg.GRPCAddr != "purser.test:19003" {
		t.Fatalf("Purser address = %q", clientCfg.GRPCAddr)
	}
	if clientCfg.Timeout != 5*time.Second {
		t.Fatalf("Purser timeout = %v, want 5s", clientCfg.Timeout)
	}
	if clientCfg.ServiceToken != "service-secret" || !clientCfg.PreferServiceToken {
		t.Fatalf("Purser authentication = token:%q prefer-service:%v", clientCfg.ServiceToken, clientCfg.PreferServiceToken)
	}
	if clientCfg.CACertFile != "/etc/frameworks/pki/ca.crt" || clientCfg.ServerName != "purser.internal" || clientCfg.AllowInsecure {
		t.Fatalf("Purser TLS = ca:%q name:%q insecure:%v", clientCfg.CACertFile, clientCfg.ServerName, clientCfg.AllowInsecure)
	}
	if clientCfg.Logger != logger {
		t.Fatal("Purser client lost the Quartermaster logger")
	}
}
