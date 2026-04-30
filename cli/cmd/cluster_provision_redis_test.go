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
	}, manifest, map[string]interface{}{}, false, "", nil, nil)
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
	}, manifest, map[string]interface{}{}, false, "", nil, nil)
	if err != nil {
		t.Fatalf("buildTaskConfig returned error: %v", err)
	}

	if got := cfg.Metadata["engine"]; got != "redis" {
		t.Fatalf("expected engine redis, got %v", got)
	}
}

func TestBuildTaskConfig_RedisInstanceConfigIncludesRoleKeys(t *testing.T) {
	manifest := &inventory.Manifest{
		Infrastructure: inventory.InfrastructureConfig{
			Redis: &inventory.RedisConfig{
				Enabled: true,
				Instances: []inventory.RedisInstance{
					{
						Name: "foghorn",
						Host: "control-1",
						Config: map[string]string{
							"bind":      "10.88.0.2",
							"maxmemory": "256mb",
						},
					},
				},
			},
		},
	}

	cfg, err := buildTaskConfig(&orchestrator.Task{
		Name:       "redis-foghorn",
		Type:       "redis",
		ServiceID:  "redis",
		InstanceID: "foghorn",
		Host:       "control-1",
		Phase:      orchestrator.PhaseInfrastructure,
	}, manifest, map[string]interface{}{}, false, "", nil, nil)
	if err != nil {
		t.Fatalf("buildTaskConfig returned error: %v", err)
	}

	if got := cfg.Metadata["bind"]; got != "10.88.0.2" {
		t.Fatalf("expected bind role key, got %v", got)
	}
	if got := cfg.Metadata["redis_bind"]; got != "10.88.0.2" {
		t.Fatalf("expected redis_bind compatibility key, got %v", got)
	}
	if got := cfg.Metadata["maxmemory"]; got != "256mb" {
		t.Fatalf("expected maxmemory role key, got %v", got)
	}
	if got := cfg.Metadata["redis_maxmemory"]; got != "256mb" {
		t.Fatalf("expected redis_maxmemory compatibility key, got %v", got)
	}
}

func TestBuildTaskConfig_RedisDefaultsBindToLoopbackAndMeshIP(t *testing.T) {
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{
			"control-1": {WireguardIP: "10.88.0.2"},
		},
		Infrastructure: inventory.InfrastructureConfig{
			Redis: &inventory.RedisConfig{
				Enabled: true,
				Instances: []inventory.RedisInstance{
					{Name: "foghorn", Host: "control-1"},
				},
			},
		},
	}

	cfg, err := buildTaskConfig(&orchestrator.Task{
		Name:       "redis-foghorn",
		Type:       "redis",
		ServiceID:  "redis",
		InstanceID: "foghorn",
		Host:       "control-1",
		Phase:      orchestrator.PhaseInfrastructure,
	}, manifest, map[string]interface{}{}, false, "", nil, nil)
	if err != nil {
		t.Fatalf("buildTaskConfig returned error: %v", err)
	}

	if got := cfg.Metadata["bind"]; got != "127.0.0.1 10.88.0.2" {
		t.Fatalf("expected Redis to bind loopback and mesh IP by default, got %v", got)
	}
}
