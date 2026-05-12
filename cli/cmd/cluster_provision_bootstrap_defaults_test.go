package cmd

import "testing"

func TestClusterProvisionBootstrapAdminDefaultsArePlatformDetails(t *testing.T) {
	cmd := newClusterProvisionCmd()

	first, err := cmd.Flags().GetString("bootstrap-admin-first-name")
	if err != nil {
		t.Fatalf("first-name flag: %v", err)
	}
	last, err := cmd.Flags().GetString("bootstrap-admin-last-name")
	if err != nil {
		t.Fatalf("last-name flag: %v", err)
	}

	if first != "FrameWorks" || last != "Operator" {
		t.Fatalf("bootstrap admin defaults = %q %q, want FrameWorks Operator", first, last)
	}
}
