package main

import (
	"context"
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	dnspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/dns"
)

func TestSyncDNSDisabledDoesNotRequireProviderDependencies(t *testing.T) {
	server := &NavigatorServer{
		DNSRecordsDisabled: true,
		Logger:             logging.NewLogger(),
	}
	response, err := server.SyncDNS(context.Background(), &dnspb.SyncDNSRequest{ServiceType: "bridge"})
	if err != nil {
		t.Fatal(err)
	}
	if !response.GetSuccess() || !strings.Contains(response.GetMessage(), "disabled") {
		t.Fatalf("unexpected disabled response: %+v", response)
	}
}
