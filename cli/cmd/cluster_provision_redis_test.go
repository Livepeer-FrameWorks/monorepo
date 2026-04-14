package cmd

import (
	"testing"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
)

func TestBuildTaskConfig_RedisEngineFromManifest(t *testing.T) {
	manifest := &inventory.Manifest{
		Infrastructure: inventory.InfrastructureConfig{
			Redis: &inventory.RedisConfig{
				Enabled: true,
				Engine:  "valkey",
				Mode:    "docker",
				Version: "8.1",
				Instances: []inventory.RedisInstance{
					{Name: "foghorn", Host: "media-1", Port: 6380},
				},
			},
		},
	}

	cfg, err := buildTaskConfig(&orchestrator.Task{
		Name:       "redis-foghorn",
		Type:       "redis",
		ServiceID:  "redis",
		InstanceID: "foghorn",
		Host:       "media-1",
		Phase:      orchestrator.PhaseInfrastructure,
	}, manifest, map[string]interface{}{}, false, "")
	if err != nil {
		t.Fatalf("buildTaskConfig returned error: %v", err)
	}

	if got := cfg.Metadata["engine"]; got != "valkey" {
		t.Fatalf("expected engine valkey, got %v", got)
	}
	if got := cfg.Version; got != "8.1" {
		t.Fatalf("expected version 8.1, got %q", got)
	}
	if got := cfg.Port; got != 6380 {
		t.Fatalf("expected port 6380, got %d", got)
	}
}

func TestBuildTaskConfig_RedisInstanceEngineOverridesManifest(t *testing.T) {
	manifest := &inventory.Manifest{
		Infrastructure: inventory.InfrastructureConfig{
			Redis: &inventory.RedisConfig{
				Enabled: true,
				Engine:  "valkey",
				Mode:    "docker",
				Instances: []inventory.RedisInstance{
					{Name: "platform", Engine: "redis", Host: "control-1", Port: 6379},
				},
			},
		},
	}

	cfg, err := buildTaskConfig(&orchestrator.Task{
		Name:       "redis-platform",
		Type:       "redis",
		ServiceID:  "redis",
		InstanceID: "platform",
		Host:       "control-1",
		Phase:      orchestrator.PhaseInfrastructure,
	}, manifest, map[string]interface{}{}, false, "")
	if err != nil {
		t.Fatalf("buildTaskConfig returned error: %v", err)
	}

	if got := cfg.Metadata["engine"]; got != "redis" {
		t.Fatalf("expected engine redis, got %v", got)
	}
}
