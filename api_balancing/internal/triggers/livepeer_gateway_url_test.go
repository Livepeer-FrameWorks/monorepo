package triggers

import (
	"testing"

	pb "frameworks/pkg/proto"
	"frameworks/pkg/servicedefs"
)

func TestLivepeerGatewayURLUsesPublicIngress(t *testing.T) {
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

	got := livepeerGatewayURLFromInstance(inst)
	want := "https://livepeer.core-central-primary.frameworks.network"
	if got != want {
		t.Fatalf("livepeerGatewayURLFromInstance() = %q, want %q", got, want)
	}
}

func TestLivepeerGatewayURLFallsBackToInstanceAddress(t *testing.T) {
	host := "10.0.0.10"
	port := int32(8935)
	inst := &pb.ServiceInstance{
		Protocol: "http",
		Host:     &host,
		Port:     &port,
	}

	got := livepeerGatewayURLFromInstance(inst)
	want := "http://10.0.0.10:8935"
	if got != want {
		t.Fatalf("livepeerGatewayURLFromInstance() = %q, want %q", got, want)
	}
}
