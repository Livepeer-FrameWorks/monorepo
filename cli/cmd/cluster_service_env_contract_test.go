package cmd

import (
	"bytes"
	"context"
	"slices"
	"sort"
	"strings"
	"testing"

	"frameworks/cli/internal/configschema"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
)

const contractTestClusterID = "core-central-primary"

func contractProductionManifest(t *testing.T, extraSecrets ...string) *inventory.Manifest {
	t.Helper()
	lines := []string{
		"DATABASE_PASSWORD=test-db-pass",
		"CLICKHOUSE_PASSWORD=test-ch-pass",
		"CHATWOOT_API_TOKEN=test-chatwoot-token",
		"CLOUDFLARE_API_TOKEN=test-cf-token",
		"CLOUDFLARE_ZONE_ID=test-zone",
		"CLOUDFLARE_ACCOUNT_ID=test-account",
		"ACME_EMAIL=ops@example.com",
		"GATEWAY_PUBLIC_URL=https://api.frameworks.network",
		"CLUSTER_ACCESS_MATERIALIZATION_SECRET=test-cluster-access",
		"FOGHORN_BALANCER_CAPABILITY_SECRET=test-balancer-capability",
		"LOOKOUT_ALERTMANAGER_TOKEN=test-lookout-alertmanager",
		"BOSUN_FIELD_ENCRYPTION_KEY=test-bosun-field-encryption-key",
		"NAVIGATOR_INTERNAL_CA_ROOT_CERT_PEM_B64=cm9vdA==",
		"NAVIGATOR_INTERNAL_CA_INTERMEDIATE_CERT_PEM_B64=aW50ZXJtZWRpYXRl",
		"NAVIGATOR_INTERNAL_CA_INTERMEDIATE_KEY_PEM_B64=a2V5",
	}
	envFile := writeTestEnvFile(t, testSharedSecrets+strings.Join(append(lines, extraSecrets...), "\n")+"\n")
	return &inventory.Manifest{
		Profile:    "production",
		RootDomain: "frameworks.network",
		EnvFiles:   []string{envFile},
		Hosts: map[string]inventory.Host{
			"central-eu-1": {ExternalIP: "10.0.0.10", Roles: []string{"control"}, WireguardIP: "10.88.0.10", WireguardPrivateKey: "wg-private-key"},
			"yuga-eu-1":    {ExternalIP: "10.0.0.11", Roles: []string{"infrastructure"}},
			"kafka-eu-1":   {ExternalIP: "10.0.0.12", Roles: []string{"infrastructure"}},
			"ch-eu-1":      {ExternalIP: "10.0.0.13", Roles: []string{"infrastructure"}},
			"ch-read-1":    {ExternalIP: "10.0.0.14", Roles: []string{"infrastructure"}},
		},
		Clusters: map[string]inventory.ClusterConfig{
			contractTestClusterID: {Name: "Core Central Primary"},
		},
		Infrastructure: inventory.InfrastructureConfig{
			Postgres: &inventory.PostgresConfig{
				Enabled: true,
				Engine:  "yugabyte",
				Port:    5433,
				Nodes:   []inventory.PostgresNode{{Host: "yuga-eu-1", ID: 1}},
			},
			ClickHouse: &inventory.ClickHouseConfig{
				Enabled:       true,
				Nodes:         []inventory.ClickHouseNode{{Host: "ch-eu-1", ID: 1}},
				ReadEndpoint:  "ch-read-1",
				WriteEndpoint: "ch-eu-1",
				Port:          9000,
			},
			Kafka: &inventory.KafkaConfig{
				Enabled:   true,
				ClusterID: contractTestClusterID,
				Brokers:   []inventory.KafkaBroker{{Host: "kafka-eu-1", ID: 1, Port: 9092}},
			},
		},
		Services: map[string]inventory.ServiceConfig{
			"bridge":          {Enabled: true, Host: "central-eu-1"},
			"commodore":       {Enabled: true, Host: "central-eu-1"},
			"quartermaster":   {Enabled: true, Host: "central-eu-1"},
			"purser":          {Enabled: true, Host: "central-eu-1"},
			"periscope-query": {Enabled: true, Host: "central-eu-1"},
			"periscope-metering": {Enabled: true, Host: "central-eu-1", Config: map[string]string{
				"METERING_SOURCE_ID":     "periscope-default",
				"METERING_SOURCE_REGION": "eu-west",
			}},
			"periscope-ingest": {Enabled: true, Host: "central-eu-1"},
			"decklog":          {Enabled: true, Host: "central-eu-1"},
			"signalman":        {Enabled: true, Host: "central-eu-1"},
			"navigator":        {Enabled: true, Host: "central-eu-1"},
			"chandler":         {Enabled: true, Host: "central-eu-1"},
			"foghorn":          {Enabled: true, Host: "central-eu-1"},
			"privateer":        {Enabled: true, Host: "central-eu-1"},
			"deckhand":         {Enabled: true, Host: "central-eu-1"},
			"skipper":          {Enabled: true, Host: "central-eu-1"},
			"steward":          {Enabled: true, Host: "central-eu-1"},
			"lookout":          {Enabled: true, Host: "central-eu-1"},
			"bosun":            {Enabled: true, Host: "central-eu-1"},
			"chatwoot":         {Enabled: true, Host: "central-eu-1", Port: 18092},
			"listmonk":         {Enabled: true, Host: "central-eu-1", Port: 9001},
		},
	}
}

func contractTask(serviceID string) *orchestrator.Task {
	phase := orchestrator.PhaseApplications
	if serviceID == "privateer" {
		phase = orchestrator.PhaseMesh
	}
	return &orchestrator.Task{
		Name:      serviceID,
		Type:      serviceID,
		ServiceID: serviceID,
		Host:      "central-eu-1",
		ClusterID: contractTestClusterID,
		Phase:     phase,
	}
}

func schemaServiceIDs(t *testing.T) []string {
	t.Helper()
	schema, err := configschema.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var ids []string
	for _, section := range schema.Services {
		if !slices.Contains(ids, section.Service) {
			ids = append(ids, section.Service)
		}
	}
	sort.Strings(ids)
	return ids
}

func TestServiceEnvContractProductionFixtureCoversEverySchemaService(t *testing.T) {
	manifest := contractProductionManifest(t)
	sharedEnv := testLoadSharedEnv(t, manifest)
	for _, serviceID := range schemaServiceIDs(t) {
		if serviceID == "helmsman" {
			// Helmsman ships only in the edge bundle, never as a cluster
			// service task. Its env contract is covered on the edge render
			// paths: TestRenderEdgeTemplatesContainerWithoutNodeIDFails,
			// TestRenderEdgeTemplatesNativeWithoutNodeIDFails, and
			// TestValidateEdgeHelmsmanEnvLayersImageEnvUnderRenderedFiles in
			// cli/internal/templates, and TestAnsibleRolesAssertHelmsmanEnvBeforeStart
			// for the Ansible edge and Helmsman roles.
			continue
		}
		t.Run(serviceID, func(t *testing.T) {
			if _, ok := manifest.Services[serviceID]; !ok {
				t.Fatalf("production fixture has no %s service; add it so the contract covers every schema service", serviceID)
			}
			task := contractTask(serviceID)
			config, err := buildTaskConfig(task, manifest, map[string]any{}, false, "", sharedEnv, nil, nil)
			if err != nil {
				t.Fatalf("buildTaskConfig: %v", err)
			}
			if err := validateTaskServiceEnvContract(manifest, task, config); err != nil {
				t.Fatalf("fully rendered production env fails the contract: %v", err)
			}
		})
	}
}

func TestProvisionTaskFailsWithoutFieldEncryptionKey(t *testing.T) {
	// A blank later value overrides the shared secret the same way an
	// operator env file leaving the key empty does.
	manifest := contractProductionManifest(t, "FIELD_ENCRYPTION_KEY=")
	sharedEnv := testLoadSharedEnv(t, manifest)
	task := contractTask("commodore")
	host, _ := manifest.GetHost(task.Host)

	_, err := provisionTask(context.Background(), task, host, nil, manifest, false, false, map[string]any{}, "", sharedEnv, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "FIELD_ENCRYPTION_KEY") {
		t.Fatalf("provision without FIELD_ENCRYPTION_KEY: err = %v, want failure naming it", err)
	}
}

// A key marked external in servicedefs is left to the deploy-without-start
// path: the contract passes, provision refuses by default, and with
// --ignore-validation the service installs stopped with the keys to add and
// the command that starts it.
func TestServiceEnvContractLeavesExternalKeysToDeployWithoutStart(t *testing.T) {
	// A blank later value overrides the fixture's token the same way an
	// operator env file leaving the key empty does.
	manifest := contractProductionManifest(t, "LOOKOUT_ALERTMANAGER_TOKEN=")
	sharedEnv := testLoadSharedEnv(t, manifest)
	task := contractTask("lookout")
	_, config, err := renderProvisionTask(task, nil, manifest, false, map[string]any{}, "", sharedEnv, nil, nil)
	if err != nil {
		t.Fatalf("contract must not fail on an external key: %v", err)
	}

	var refused bytes.Buffer
	err = deferStartForMissingExternalEnv(&refused, task, &config, false)
	if err == nil || !strings.Contains(err.Error(), "LOOKOUT_ALERTMANAGER_TOKEN") || config.DeferStart {
		t.Fatalf("without --ignore-validation: err = %v, DeferStart = %v; want refusal naming the key", err, config.DeferStart)
	}

	var deferred bytes.Buffer
	if err := deferStartForMissingExternalEnv(&deferred, task, &config, true); err != nil {
		t.Fatalf("with --ignore-validation: %v", err)
	}
	if !config.DeferStart {
		t.Fatal("with --ignore-validation the service must deploy without starting")
	}
	for _, want := range []string{"Add LOOKOUT_ALERTMANAGER_TOKEN", "start it with: frameworks cluster provision --only applications"} {
		if !strings.Contains(deferred.String(), want) {
			t.Fatalf("deferred output %q lacks %q", deferred.String(), want)
		}
	}
}

// Every schema-required key that is not external still fails the contract,
// including Bosun's operator-generated field-encryption key.
func TestServiceEnvContractNonExternalKeysStillFailHard(t *testing.T) {
	manifest := contractProductionManifest(t, "BOSUN_FIELD_ENCRYPTION_KEY=")
	sharedEnv := testLoadSharedEnv(t, manifest)
	task := contractTask("bosun")
	_, _, err := renderProvisionTask(task, nil, manifest, false, map[string]any{}, "", sharedEnv, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "BOSUN_FIELD_ENCRYPTION_KEY") {
		t.Fatalf("bosun without BOSUN_FIELD_ENCRYPTION_KEY: err = %v, want contract failure naming it", err)
	}
}

func TestServiceEnvContractDevProfileStillRequiresSchemaKeys(t *testing.T) {
	envFile := writeTestEnvFile(t, "JWT_SECRET=dev-jwt\nFIELD_ENCRYPTION_KEY=dev-enc\nUSAGE_HASH_SECRET=dev-usage\nDATABASE_URL=postgres://commodore@db:5432/commodore\n")
	manifest := &inventory.Manifest{
		Profile:  "dev",
		EnvFiles: []string{envFile},
		Hosts:    map[string]inventory.Host{"central-eu-1": {ExternalIP: "10.0.0.10"}},
		Services: map[string]inventory.ServiceConfig{"commodore": {Enabled: true, Host: "central-eu-1"}},
	}
	sharedEnv := testLoadSharedEnv(t, manifest)
	task := contractTask("commodore")
	task.ClusterID = ""

	config, err := buildTaskConfig(task, manifest, map[string]any{}, false, "", sharedEnv, nil, nil)
	if err != nil {
		t.Fatalf("buildTaskConfig: %v", err)
	}
	err = validateTaskServiceEnvContract(manifest, task, config)
	if err == nil || !strings.Contains(err.Error(), "SERVICE_TOKEN") {
		t.Fatalf("dev manifest without SERVICE_TOKEN: err = %v, want failure naming SERVICE_TOKEN", err)
	}

	config.EnvVars["SERVICE_TOKEN"] = "dev-token"
	if err := validateTaskServiceEnvContract(manifest, task, config); err != nil {
		t.Fatalf("dev manifest with every schema key must pass without the production rules: %v", err)
	}
}

func TestServiceEnvContractPrivateerPassesOnlyThroughRoleAddedKeyFile(t *testing.T) {
	manifest := contractProductionManifest(t)
	sharedEnv := testLoadSharedEnv(t, manifest)
	task := contractTask("privateer")
	config, err := buildTaskConfig(task, manifest, map[string]any{}, false, "", sharedEnv, nil, nil)
	if err != nil {
		t.Fatalf("buildTaskConfig: %v", err)
	}
	if strings.TrimSpace(config.EnvVars["MESH_PRIVATE_KEY_FILE"]) != "" {
		t.Fatal("task env already carries MESH_PRIVATE_KEY_FILE; this test needs the role to add it")
	}
	err = validateServiceEnvContract(manifest, "privateer", config.EnvVars)
	if err == nil || !strings.Contains(err.Error(), "MESH_PRIVATE_KEY_FILE") {
		t.Fatalf("task env alone: err = %v, want failure naming MESH_PRIVATE_KEY_FILE", err)
	}
	if err := validateTaskServiceEnvContract(manifest, task, config); err != nil {
		t.Fatalf("final Privateer env must pass through the role-added key file: %v", err)
	}
}

// The secret-restriction steps delete keys from services that must not hold
// them. None may delete a key the schema requires for the service it runs on.
func TestSecretRestrictionsKeepSchemaRequiredKeys(t *testing.T) {
	schema, err := configschema.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	all := map[string]string{}
	for _, section := range schema.Services {
		for _, v := range section.Variables {
			if v.Required {
				all[v.Key] = "set"
			}
		}
	}
	for _, serviceID := range schemaServiceIDs(t) {
		for _, provider := range []string{"", "carto", "openfreemap"} {
			env := map[string]string{}
			for key, value := range all {
				env[key] = value
			}
			env["BASEMAP_PROVIDER"] = provider
			restrictClusterAccessSecrets(serviceID, env)
			restrictBasemapCredentials(serviceID, env)
			restrictMeteringSourceIdentity(serviceID, env)
			if missing := schema.Missing(serviceID, env); len(missing) > 0 {
				t.Errorf("%s (BASEMAP_PROVIDER=%q): restriction deletes schema-required %v", serviceID, provider, missing)
			}
		}
	}
}

func TestDatabaseURLHasPassword(t *testing.T) {
	for raw, want := range map[string]bool{
		"postgres://commodore:secret@db.internal:5433/commodore?sslmode=disable":  true,
		"postgres://commodore:s%2Fcret@a.internal:5433,b.internal:5433/commodore": true,
		"postgres://commodore@db.internal:5433/commodore?sslmode=disable":         false,
		"postgres://commodore:@db.internal:5433/commodore":                        false,
		"host=db.internal user=commodore password=secret dbname=commodore":        true,
		"host=db.internal user=commodore dbname=commodore":                        false,
		"": false,
		"postgres://db.internal:5433/commodore?user=commodore&password=in-query-string": false,
	} {
		if got := databaseURLHasPassword(raw); got != want {
			t.Errorf("databaseURLHasPassword(%q) = %v, want %v", raw, got, want)
		}
	}
}

// The provisioner builds DATABASE_URL from DATABASE_HOST even without a
// password, so the final env must not satisfy the credentials rule by the
// generated URL alone.
func TestServiceEnvContractProductionDatabaseRuleIgnoresPasswordlessGeneratedURL(t *testing.T) {
	manifest := contractProductionManifest(t, "DATABASE_PASSWORD=")
	sharedEnv := testLoadSharedEnv(t, manifest)
	task := contractTask("purser")
	config, err := buildTaskConfig(task, manifest, map[string]any{}, false, "", sharedEnv, nil, nil)
	if err != nil {
		t.Fatalf("buildTaskConfig: %v", err)
	}
	if strings.TrimSpace(config.EnvVars["DATABASE_URL"]) == "" {
		t.Fatal("fixture must generate DATABASE_URL from DATABASE_HOST")
	}
	err = validateTaskServiceEnvContract(manifest, task, config)
	if err == nil || !strings.Contains(err.Error(), "DATABASE_PASSWORD") {
		t.Fatalf("passwordless generated URL: err = %v, want DATABASE_PASSWORD failure", err)
	}
}
