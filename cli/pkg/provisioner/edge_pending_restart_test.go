package provisioner

import (
	"slices"
	"strings"
	"testing"
)

// The CLI reads back the markers the roles write; the paths must agree.
func TestRestartPendingMarkerPathsMatchRoles(t *testing.T) {
	mist := readRepoFile(t, mistserverRoleDir+"defaults/main.yml")
	edge := readRepoFile(t, edgeRoleDir+"defaults/main.yml")
	paths := map[string]bool{}
	for _, m := range edgeRestartPendingMarkers {
		paths[m.Path] = true
	}
	for _, want := range []string{
		"/opt/frameworks/mistserver/.restart-pending",
		"/opt/frameworks/caddy/.restart-pending",
		"/usr/local/opt/frameworks/mistserver/.restart-pending",
		"$HOME/.local/opt/frameworks/mistserver/.restart-pending",
	} {
		if !paths[want] {
			t.Fatalf("CLI does not read marker %s", want)
		}
	}
	if !strings.Contains(mist, "else mistserver_install_dir }}/.restart-pending") || !strings.Contains(mist, "mistserver_install_dir: /opt/frameworks/mistserver") {
		t.Fatalf("mistserver_restart_pending_marker no longer resolves to /opt/frameworks/mistserver/.restart-pending")
	}
	if !strings.Contains(mist, "mistserver_darwin_system_base_dir: /usr/local/opt/frameworks") || !strings.Contains(mist, `mistserver_darwin_user_base_dir: "{{ ansible_facts.env.HOME }}/.local/opt/frameworks"`) {
		t.Fatalf("Darwin MistServer base dirs moved; update edgeRestartPendingMarkers")
	}
	if !strings.Contains(edge, "edge_caddy_restart_pending_marker: /opt/frameworks/caddy/.restart-pending") {
		t.Fatalf("edge_caddy_restart_pending_marker moved; update edgeRestartPendingMarkers")
	}
	if got := parsePendingRestartMarkers("0\n3\n2\nnoise\n9\n"); !slices.Equal(got, []string{"frameworks-mistserver", "MistServer (launchd)"}) {
		t.Fatalf("parsePendingRestartMarkers = %q", got)
	}
}
