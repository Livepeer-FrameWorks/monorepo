package cmd

import (
	"context"
	"strings"
	"testing"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/gitops"
	"frameworks/cli/pkg/inventory"
)

func TestVerifyAuthReleaseConvergenceRejectsHealthyStaleChartroom(t *testing.T) {
	t.Setenv("FRAMEWORKS_IMAGE_REGISTRY", "dockerhub")
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{
			"core":  {Name: "core"},
			"web-1": {Name: "web-1"},
			"web-2": {Name: "web-2"},
		},
		Services: map[string]inventory.ServiceConfig{
			"commodore": {Enabled: true, Host: "core"},
			"bridge":    {Enabled: true, Host: "core"},
		},
		Interfaces: map[string]inventory.ServiceConfig{
			"chartroom": {Enabled: true, Hosts: []string{"web-1", "web-2"}},
		},
	}
	release := &gitops.Manifest{
		PlatformVersion: "v0.3.10",
		Services: []gitops.ServiceEntry{
			{Name: "commodore", ServiceVersion: "v0.3.10"},
			{Name: "bridge", ServiceVersion: "v0.3.10"},
		},
		Interfaces: []gitops.InterfaceEntry{{Name: "chartroom", ServiceVersion: "v0.3.10", Digest: "sha256:new"}},
	}
	inspect := func(_ context.Context, host inventory.Host, service string) (*detect.ServiceState, error) {
		state := &detect.ServiceState{Exists: true, Running: true, Version: "v0.3.10", Mode: "native"}
		if service == "chartroom" {
			state.Mode = "docker"
			state.Metadata = map[string]string{"image": "chartroom:v0.3.10@sha256:new"}
			if host.Name == "web-2" {
				state.Version = "v0.3.9"
			}
		}
		return state, nil
	}
	err := verifyAuthReleaseConvergence(context.Background(), manifest, release, inspect)
	if err == nil || !strings.Contains(err.Error(), "chartroom@web-2: running v0.3.9") {
		t.Fatalf("stale frontend was accepted: %v", err)
	}

	inspect = func(_ context.Context, _ inventory.Host, service string) (*detect.ServiceState, error) {
		state := &detect.ServiceState{Exists: true, Running: true, Version: "v0.3.10", Mode: "native"}
		if service == "chartroom" {
			state.Mode = "docker"
			state.Metadata = map[string]string{"image": "chartroom:v0.3.10@sha256:new"}
		}
		return state, nil
	}
	if err := verifyAuthReleaseConvergence(context.Background(), manifest, release, inspect); err != nil {
		t.Fatalf("converged auth services were refused: %v", err)
	}
}

func TestVerifyAuthReleaseConvergenceRejectsWrongImageDigest(t *testing.T) {
	t.Setenv("FRAMEWORKS_IMAGE_REGISTRY", "dockerhub")
	manifest := &inventory.Manifest{
		Hosts:      map[string]inventory.Host{"web": {Name: "web"}},
		Interfaces: map[string]inventory.ServiceConfig{"chartroom": {Enabled: true, Host: "web"}},
	}
	release := &gitops.Manifest{
		PlatformVersion: "v0.3.10",
		Interfaces:      []gitops.InterfaceEntry{{Name: "chartroom", ServiceVersion: "v0.3.10", Digest: "sha256:new"}},
	}
	err := verifyAuthReleaseConvergence(context.Background(), manifest, release, func(context.Context, inventory.Host, string) (*detect.ServiceState, error) {
		return &detect.ServiceState{Exists: true, Running: true, Mode: "docker", Version: "v0.3.10", Metadata: map[string]string{"image": "chartroom:v0.3.10@sha256:old"}}, nil
	})
	if err == nil || !strings.Contains(err.Error(), "image digest") {
		t.Fatalf("wrong image digest was accepted: %v", err)
	}
}

func TestVerifyAuthReleaseConvergenceUsesSelectedRegistryDigest(t *testing.T) {
	t.Setenv("FRAMEWORKS_IMAGE_REGISTRY", "ghcr")
	manifest := &inventory.Manifest{
		Hosts:      map[string]inventory.Host{"web": {Name: "web"}},
		Interfaces: map[string]inventory.ServiceConfig{"chartroom": {Enabled: true, Host: "web"}},
	}
	release := &gitops.Manifest{
		PlatformVersion: "v0.3.10",
		Interfaces: []gitops.InterfaceEntry{{
			Name: "chartroom", ServiceVersion: "v0.3.10", Digest: "sha256:dockerhub",
			Images: map[string]gitops.RegistryImage{
				"ghcr": {Image: "ghcr.io/frameworks/chartroom:v0.3.10", Digest: "sha256:ghcr"},
			},
		}},
	}
	err := verifyAuthReleaseConvergence(context.Background(), manifest, release, func(context.Context, inventory.Host, string) (*detect.ServiceState, error) {
		return &detect.ServiceState{Exists: true, Running: true, Mode: "docker", Version: "v0.3.10", Metadata: map[string]string{"image": "ghcr.io/frameworks/chartroom:v0.3.10@sha256:ghcr"}}, nil
	})
	if err != nil {
		t.Fatalf("selected registry image was refused: %v", err)
	}
}
