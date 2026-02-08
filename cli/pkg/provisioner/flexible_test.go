package provisioner

import (
	"testing"

	"frameworks/cli/pkg/detect"
)

func TestShouldSkipProvisionWithMatchingImage(t *testing.T) {
	state := &detect.ServiceState{
		Exists:  true,
		Running: true,
		Metadata: map[string]string{
			"image": "example.com/service@sha256:abc",
		},
	}

	skip, _ := shouldSkipProvision(state, ServiceConfig{}, "", "example.com/service@sha256:abc")
	if !skip {
		t.Fatalf("expected skip when image matches")
	}
}

func TestShouldSkipProvisionWithMatchingVersion(t *testing.T) {
	state := &detect.ServiceState{
		Exists:  true,
		Running: true,
		Version: "v1.2.3",
	}

	skip, _ := shouldSkipProvision(state, ServiceConfig{}, "v1.2.3", "")
	if !skip {
		t.Fatalf("expected skip when version matches")
	}
}

func TestShouldSkipProvisionWithMismatchedVersion(t *testing.T) {
	state := &detect.ServiceState{
		Exists:  true,
		Running: true,
		Version: "v1.2.3",
	}

	skip, _ := shouldSkipProvision(state, ServiceConfig{}, "v2.0.0", "")
	if skip {
		t.Fatalf("expected no skip when version mismatches")
	}
}

func TestShouldSkipProvisionForced(t *testing.T) {
	state := &detect.ServiceState{
		Exists:  true,
		Running: true,
		Version: "v1.2.3",
	}

	skip, _ := shouldSkipProvision(state, ServiceConfig{Force: true}, "v1.2.3", "")
	if skip {
		t.Fatalf("expected no skip when force is set")
	}
}
