package cmd

import (
	"slices"
	"testing"

	"frameworks/cli/pkg/inventory"
)

func TestListenersInEphemeralRange(t *testing.T) {
	planned := []plannedListener{
		{Port: 18006, Owner: "decklog"},
		{Port: 26379, Owner: "sentinel"},
		{Port: 51820, Proto: "udp", Owner: "wireguard"},
	}
	ports := func(ls []plannedListener) []int {
		var out []int
		for _, l := range ls {
			out = append(out, l.Port)
		}
		return out
	}
	for name, tc := range map[string]struct {
		probe string
		want  []int
	}{
		// What staging ran when Decklog lost 18006 to a source port.
		"wide range, nothing reserved":       {probe: "10000\t65535\n\n", want: []int{18006, 26379, 51820}},
		"kernel default, nothing reserved":   {probe: "32768\t60999\n\n", want: []int{51820}},
		"kernel default, wireguard reserved": {probe: "32768\t60999\n51820\n", want: nil},
		"wide range, ranges reserved":        {probe: "10000\t65535\n18000-19999,26379,51820\n", want: nil},
	} {
		low, high, reserved, err := parseEphemeralPorts(tc.probe)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := ports(listenersInEphemeralRange(low, high, reserved, planned)); !slices.Equal(got, tc.want) {
			t.Errorf("%s: exposed ports %v, want %v", name, got, tc.want)
		}
	}
}

func TestHostFixedListenersIncludeWireGuard(t *testing.T) {
	manifest := &inventory.Manifest{Hosts: map[string]inventory.Host{"mesh": {Name: "mesh", WireguardIP: "10.0.0.1"}}}
	got := hostFixedListeners(manifest, map[string]map[int]string{"mesh": {18006: "decklog"}}, "mesh")
	var wg bool
	for _, l := range got {
		if l.Port == 51820 && l.Proto == "udp" {
			wg = true
		}
	}
	if !wg || len(got) != 2 {
		t.Fatalf("fixed listeners %+v, want decklog and the WireGuard port", got)
	}
}
