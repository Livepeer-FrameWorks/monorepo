package triggers

import (
	"testing"

	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
)

func TestLivepeerGatewayURLUsesPhysicalInstanceHost(t *testing.T) {
	inst := &pb.ServiceInstance{
		Metadata: map[string]string{
			servicedefs.LivepeerGatewayMetadataPublicInstanceHost: "livepeer-gateway.regional-eu-2.infra.frameworks.network",
		},
	}

	got := livepeerGatewayURLFromInstance(inst)
	want := "https://livepeer-gateway.regional-eu-2.infra.frameworks.network"
	if got != want {
		t.Fatalf("livepeerGatewayURLFromInstance() = %q, want %q", got, want)
	}
}

// Physical-only contract: an instance that only advertises a pooled public_host
// (or a raw advertise host) has no per-instance failover endpoint and must be
// excluded from the broadcaster list.
func TestLivepeerGatewayURLExcludesPooledAndRawOnlyInstances(t *testing.T) {
	host := "10.0.0.10"
	port := int32(8935)
	inst := &pb.ServiceInstance{
		Protocol: "http",
		Host:     &host,
		Port:     &port,
		Metadata: map[string]string{
			servicedefs.LivepeerGatewayMetadataPublicHost: "livepeer.core-central-primary.frameworks.network",
			servicedefs.LivepeerGatewayMetadataPublicPort: "8935",
		},
	}

	if got := livepeerGatewayURLFromInstance(inst); got != "" {
		t.Fatalf("livepeerGatewayURLFromInstance() = %q, want \"\" (no physical endpoint)", got)
	}
}
