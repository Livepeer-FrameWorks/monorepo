package scheduler

import "testing"

func TestNormalizeSourceIdentity(t *testing.T) {
	id, region, err := NormalizeSourceIdentity("  periscope-default  ", " eu-west ")
	if err != nil {
		t.Fatalf("NormalizeSourceIdentity: %v", err)
	}
	if id != "periscope-default" || region != "eu-west" {
		t.Fatalf("identity = %q/%q", id, region)
	}

	for name, values := range map[string][2]string{
		"missing id":       {"", "eu-west"},
		"invalid id":       {"Periscope EU", "eu-west"},
		"missing region":   {"periscope-default", ""},
		"invalid region":   {"periscope-default", "EU West"},
		"oversized region": {"periscope-default", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := NormalizeSourceIdentity(values[0], values[1]); err == nil {
				t.Fatal("invalid identity accepted")
			}
		})
	}
}
