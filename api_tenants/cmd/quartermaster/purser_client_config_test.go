package main

import (
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func TestQuartermasterPurserClientUsesBoundedPureServiceAuthentication(t *testing.T) {
	logger := logging.NewLoggerWithService("quartermaster-test")
	cfg := quartermasterPurserClientConfig("purser.test:19003", "service-secret", logger)
	if cfg.GRPCAddr != "purser.test:19003" {
		t.Fatalf("Purser address = %q", cfg.GRPCAddr)
	}
	if cfg.Timeout != 5*time.Second {
		t.Fatalf("Purser timeout = %v, want 5s", cfg.Timeout)
	}
	if cfg.ServiceToken != "service-secret" || !cfg.PreferServiceToken {
		t.Fatalf("Purser authentication = token:%q prefer-service:%v", cfg.ServiceToken, cfg.PreferServiceToken)
	}
	if cfg.Logger != logger {
		t.Fatal("Purser client lost the Quartermaster logger")
	}
}
