package cmd

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestStringSliceFlag(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().StringSlice("repos", nil, "")

	// Unset flag → nil.
	if got := stringSliceFlag(cmd, "repos"); got != nil {
		t.Errorf("unset flag = %v, want nil", got)
	}
	// Flag that doesn't exist → nil.
	if got := stringSliceFlag(cmd, "absent"); got != nil {
		t.Errorf("absent flag = %v, want nil", got)
	}

	// Empty entries (e.g. a trailing/double comma) are dropped.
	if err := cmd.Flags().Set("repos", "a,,b"); err != nil {
		t.Fatalf("set: %v", err)
	}
	got := stringSliceFlag(cmd, "repos")
	want := []string{"a", "b"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
