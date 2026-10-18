package servicedefs

import "testing"

func TestAuxiliaryPorts(t *testing.T) {
	ports := AuxiliaryPorts("foghorn")
	// Foghorn binds its internal HTTP port and the external gRPC port edges
	// dial from outside the mesh, besides its primary and gRPC ports.
	if len(ports) != 2 || ports[0].Port != FoghornInternalHTTPPort || ports[0].Name != "foghorn-internal-http" ||
		ports[1].Port != FoghornExternalGRPCPort || ports[1].Name != "foghorn-external-grpc" {
		t.Fatalf("unexpected Foghorn auxiliary ports: %+v", ports)
	}
	ports[0].Port = 1
	if got := AuxiliaryPorts("foghorn")[0].Port; got != FoghornInternalHTTPPort {
		t.Fatalf("caller mutated auxiliary port catalog: %d", got)
	}
	if ports := AuxiliaryPorts("unknown"); len(ports) != 0 {
		t.Fatalf("unknown service has auxiliary ports: %+v", ports)
	}
}
