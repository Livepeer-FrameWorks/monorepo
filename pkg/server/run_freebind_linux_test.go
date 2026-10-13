package server

import (
	"net"
	"net/http"
	"testing"
)

// A FreeBind listener binds an address no interface holds, which a mesh-address
// listener needs when its service starts before WireGuard is up.
func TestBindListenersFreeBindAcceptsUnassignedAddress(t *testing.T) {
	const unassigned = "192.0.2.123" // TEST-NET-1, never assigned to a host
	httpRunning, _, err := bindListeners(RunSpec{HTTP: []HTTPListener{{
		Name: "mesh", BindAddr: unassigned, Port: "0", FreeBind: true, Handler: http.NotFoundHandler(),
	}}})
	if err != nil {
		t.Fatalf("bind %s with FreeBind: %v", unassigned, err)
	}
	for _, r := range httpRunning {
		_ = r.lis.Close()
	}

	if _, _, err := bindListeners(RunSpec{HTTP: []HTTPListener{{
		Name: "strict", BindAddr: unassigned, Port: "0", Handler: http.NotFoundHandler(),
	}}}); err == nil {
		t.Fatalf("bind %s without FreeBind succeeded; want the kernel to refuse an unassigned address", unassigned)
	}
}

func TestBindListenersFreeBindAcceptsUnassignedIPv6Address(t *testing.T) {
	probe, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("no IPv6 on this host: %v", err)
	}
	_ = probe.Close()
	const unassigned = "2001:db8::123" // documentation prefix, never assigned to a host
	httpRunning, _, err := bindListeners(RunSpec{HTTP: []HTTPListener{{
		Name: "mesh", BindAddr: unassigned, Port: "0", FreeBind: true, Handler: http.NotFoundHandler(),
	}}})
	if err != nil {
		t.Fatalf("bind %s with FreeBind: %v", unassigned, err)
	}
	for _, r := range httpRunning {
		_ = r.lis.Close()
	}
}
