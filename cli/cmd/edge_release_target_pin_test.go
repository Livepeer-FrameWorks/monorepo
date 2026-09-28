package cmd

import (
	"testing"

	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

// Provisioning re-syncs the edge target with the manifest channel. That must
// not replace the exact version `cluster release apply` pinned, or a newer
// channel build reaches the edges before the control plane runs it.
func TestRetainedPinnedVersion(t *testing.T) {
	pinned := &quartermasterpb.ClusterReleaseTarget{ClusterId: "c1", Channel: "rc", TargetVersion: "v0.3.11-rc9"}
	tracking := &quartermasterpb.ClusterReleaseTarget{ClusterId: "c1", Channel: "rc"}
	cases := []struct {
		name      string
		existing  *quartermasterpb.ClusterReleaseTarget
		channel   string
		requested string
		want      string
	}{
		{"channel track keeps the release pin", pinned, "rc", "", "v0.3.11-rc9"},
		{"explicit version replaces the pin", pinned, "rc", "v0.3.11-rc10", "v0.3.11-rc10"},
		{"channel change tracks the new channel", pinned, "stable", "", ""},
		{"tracking target keeps tracking", tracking, "rc", "", ""},
		{"no target yet tracks the channel", nil, "rc", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := retainedPinnedVersion(tc.existing, tc.channel, tc.requested); got != tc.want {
				t.Fatalf("retainedPinnedVersion = %q, want %q", got, tc.want)
			}
		})
	}
}
