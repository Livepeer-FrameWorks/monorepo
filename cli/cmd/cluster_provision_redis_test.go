package cmd

import (
	"strings"
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
	}, manifest, map[string]interface{}{}, false, "", nil, nil, nil)
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
	}, manifest, map[string]interface{}{}, false, "", nil, nil, nil)
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
	}, manifest, map[string]interface{}{}, false, "", nil, nil, nil)
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
	}, manifest, map[string]interface{}{}, false, "", nil, nil, nil)
	if err != nil {
		t.Fatalf("buildTaskConfig returned error: %v", err)
	}

	if got := cfg.Metadata["bind"]; got != "127.0.0.1 10.88.0.2" {
		t.Fatalf("expected Redis to bind loopback and mesh IP by default, got %v", got)
	}
}

func TestBuildTaskConfig_RedisDuplicateNamesUseTaskClusterAndHost(t *testing.T) {
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{
			"regional-eu-1": {WireguardIP: "10.88.158.227"},
			"regional-us-1": {WireguardIP: "10.88.236.29"},
		},
		Infrastructure: inventory.InfrastructureConfig{
			Redis: &inventory.RedisConfig{
				Enabled: true,
				Instances: []inventory.RedisInstance{
					{Name: "foghorn", Cluster: "media-eu-1", Host: "regional-eu-1", Port: 6379},
					{Name: "foghorn", Cluster: "media-us-1", Host: "regional-us-1", Port: 6379},
				},
			},
		},
	}

	cfg, err := buildTaskConfig(&orchestrator.Task{
		Name:       "redis-foghorn",
		Type:       "redis",
		ServiceID:  "redis",
		InstanceID: "foghorn",
		Host:       "regional-us-1",
		Phase:      orchestrator.PhaseInfrastructure,
		ClusterID:  "media-us-1",
	}, manifest, map[string]interface{}{}, false, "", nil, nil, nil)
	if err != nil {
		t.Fatalf("buildTaskConfig returned error: %v", err)
	}

	if got := cfg.Metadata["bind"]; got != "127.0.0.1 10.88.236.29" {
		t.Fatalf("expected US Redis task to bind US mesh IP, got %v", got)
	}
}

func TestBuildTaskConfig_ClusterScopedServiceAliasesUseTaskConfig(t *testing.T) {
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{
			"regional-eu-1": {WireguardIP: "10.88.158.227"},
			"regional-us-1": {WireguardIP: "10.88.236.29"},
			"regional-us-2": {WireguardIP: "10.88.64.31"},
		},
		Services: map[string]inventory.ServiceConfig{
			"chandler-eu": {Enabled: true, Deploy: "chandler", Hosts: []string{"regional-eu-1"}, Cluster: "media-eu-1"},
			"chandler-us": {Enabled: true, Deploy: "chandler", Hosts: []string{"regional-us-1", "regional-us-2"}, Cluster: "media-us-1"},
			"foghorn-eu":  {Enabled: true, Deploy: "foghorn", Hosts: []string{"regional-eu-1"}, Cluster: "media-eu-1", Config: map[string]string{"CELL": "eu"}},
			"foghorn-us":  {Enabled: true, Deploy: "foghorn", Hosts: []string{"regional-us-1", "regional-us-2"}, Cluster: "media-us-1", Config: map[string]string{"CELL": "us"}},
			"livepeer-gateway-us": {
				Enabled: true,
				Deploy:  "livepeer-gateway",
				Hosts:   []string{"regional-us-1", "regional-us-2"},
				Cluster: "media-us-1",
				Config:  map[string]string{"http_addr": "0.0.0.0:8935"},
			},
		},
	}

	foghornCfg, err := buildTaskConfig(&orchestrator.Task{
		Name:       "foghorn-us@regional-us-2",
		Type:       "foghorn",
		ServiceID:  "foghorn-us",
		InstanceID: "regional-us-2",
		Host:       "regional-us-2",
		Phase:      orchestrator.PhaseApplications,
		ClusterID:  "media-us-1",
	}, manifest, map[string]interface{}{}, false, "", nil, nil, nil)
	if err != nil {
		t.Fatalf("buildTaskConfig for foghorn alias returned error: %v", err)
	}
	if got := foghornCfg.Metadata["CELL"]; got != "us" {
		t.Fatalf("expected foghorn-us metadata, got %v", got)
	}
	if got := foghornCfg.EnvVars["CELL"]; got != "us" {
		t.Fatalf("expected foghorn-us env config, got %v", got)
	}
	if got := foghornCfg.EnvVars["CHANDLER_INTERNAL_URL"]; strings.Contains(got, "regional-eu-1.internal") || !strings.Contains(got, "regional-us-1.internal") {
		t.Fatalf("expected Chandler URL to use US Chandler hosts, got %q", got)
	}

	gatewayCfg, err := buildTaskConfig(&orchestrator.Task{
		Name:       "livepeer-gateway-us@regional-us-2",
		Type:       "livepeer-gateway",
		ServiceID:  "livepeer-gateway-us",
		InstanceID: "regional-us-2",
		Host:       "regional-us-2",
		Phase:      orchestrator.PhaseApplications,
		ClusterID:  "media-us-1",
	}, manifest, map[string]interface{}{}, false, "", nil, nil, nil)
	if err != nil {
		t.Fatalf("buildTaskConfig for gateway alias returned error: %v", err)
	}
	if got := gatewayCfg.EnvVars["http_addr"]; got != "0.0.0.0:8935" {
		t.Fatalf("expected gateway alias inline config, got %q", got)
	}
}

func TestBuildTaskConfig_RedisPasswordFromSharedEnv(t *testing.T) {
	manifest := &inventory.Manifest{
		Infrastructure: inventory.InfrastructureConfig{
			Redis: &inventory.RedisConfig{
				Enabled: true,
				Instances: []inventory.RedisInstance{
					{Name: "chatwoot", Host: "control-1", Password: "${REDIS_CHATWOOT_PASSWORD}"},
				},
			},
		},
	}

	cfg, err := buildTaskConfig(&orchestrator.Task{
		Name:       "redis-chatwoot",
		Type:       "redis",
		ServiceID:  "redis",
		InstanceID: "chatwoot",
		Host:       "control-1",
		Phase:      orchestrator.PhaseInfrastructure,
	}, manifest, map[string]interface{}{}, false, "", map[string]string{
		"REDIS_CHATWOOT_PASSWORD": "redis secret",
	}, nil, nil)
	if err != nil {
		t.Fatalf("buildTaskConfig returned error: %v", err)
	}

	if got := cfg.Metadata["password"]; got != "redis secret" {
		t.Fatalf("expected Redis role password from shared env, got %v", got)
	}
}

func TestBuildTaskConfig_RedisPasswordFromSharedEnvFallback(t *testing.T) {
	manifest := &inventory.Manifest{
		Infrastructure: inventory.InfrastructureConfig{
			Redis: &inventory.RedisConfig{
				Enabled: true,
				Instances: []inventory.RedisInstance{
					{Name: "chatwoot", Host: "control-1"},
				},
			},
		},
	}

	cfg, err := buildTaskConfig(&orchestrator.Task{
		Name:       "redis-chatwoot",
		Type:       "redis",
		ServiceID:  "redis",
		InstanceID: "chatwoot",
		Host:       "control-1",
		Phase:      orchestrator.PhaseInfrastructure,
	}, manifest, map[string]interface{}{}, false, "", map[string]string{
		"REDIS_CHATWOOT_PASSWORD": "redis secret",
	}, nil, nil)
	if err != nil {
		t.Fatalf("buildTaskConfig returned error: %v", err)
	}

	if got := cfg.Metadata["password"]; got != "redis secret" {
		t.Fatalf("expected Redis role password from shared env fallback, got %v", got)
	}
}
