package qmbootstrap

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
)

func TestNewServiceRequestAdvertisesReadinessPath(t *testing.T) {
	req, err := NewServiceRequest(ServiceRegistration{
		ServiceType:   "commodore",
		Port:          "18001",
		AdvertiseHost: "commodore",
		NodeID:        "node-1",
	})
	if err != nil {
		t.Fatalf("NewServiceRequest: %v", err)
	}
	def, _ := servicedefs.Lookup("commodore")
	if req.GetHealthEndpoint() != def.ReadinessPath() {
		t.Fatalf("health endpoint = %q, want readiness path %q", req.GetHealthEndpoint(), def.ReadinessPath())
	}
	if req.GetType() != "commodore" || req.GetProtocol() != "http" || req.GetPort() != 18001 || req.GetAdvertiseHost() != "commodore" {
		t.Fatalf("unexpected request: %+v", req)
	}
	if req.ClusterId != nil {
		t.Fatalf("empty cluster must stay unset, got %q", req.GetClusterId())
	}
	if req.GetNodeId() != "node-1" {
		t.Fatalf("node id = %q", req.GetNodeId())
	}
}

func TestNewServiceRequestSupportsGRPCRegistrationWithoutHealthEndpoint(t *testing.T) {
	req, err := NewServiceRequest(ServiceRegistration{
		ServiceType:        "signalman",
		Protocol:           "grpc",
		Port:               "19005",
		AdvertiseHost:      "signalman",
		OmitHealthEndpoint: true,
	})
	if err != nil {
		t.Fatalf("NewServiceRequest: %v", err)
	}
	if req.GetProtocol() != "grpc" || req.GetPort() != 19005 {
		t.Fatalf("protocol/port = %q/%d", req.GetProtocol(), req.GetPort())
	}
	if req.HealthEndpoint != nil {
		t.Fatalf("health endpoint must be omitted, got %q", req.GetHealthEndpoint())
	}
}

func TestNewServiceRequestRejectsUnknownServiceAndBadPort(t *testing.T) {
	if _, err := NewServiceRequest(ServiceRegistration{ServiceType: "nope", Port: "18001"}); err == nil {
		t.Fatal("unknown service must be rejected")
	}
	for _, port := range []string{"", "0", "70000", "http"} {
		if _, err := NewServiceRequest(ServiceRegistration{ServiceType: "bridge", Port: port}); err == nil {
			t.Fatalf("port %q must be rejected", port)
		}
	}
}
