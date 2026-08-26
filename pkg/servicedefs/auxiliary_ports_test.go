package servicedefs

import "testing"

func TestAuxiliaryPorts(t *testing.T) {
	ports := AuxiliaryPorts("foghorn")
	if len(ports) != 1 || ports[0].Port != FoghornInternalHTTPPort || ports[0].Name != "foghorn-internal-http" {
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
