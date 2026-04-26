package cmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
	pb "frameworks/pkg/proto"
	"frameworks/pkg/servicedefs"

	"github.com/spf13/cobra"
)

// testSharedSecrets provides the required shared platform secrets for test env files.
const testSharedSecrets = "SERVICE_TOKEN=test-token\nJWT_SECRET=test-jwt\nPASSWORD_RESET_SECRET=test-reset\nFIELD_ENCRYPTION_KEY=test-enc\nUSAGE_HASH_SECRET=test-hash\n"

type fakeFoghornClusterAssigner struct {
	calls  []string
	errFor map[string]error
}

func (f *fakeFoghornClusterAssigner) AssignFoghornToCluster(_ context.Context, req *pb.AssignFoghornToClusterRequest) error {
	f.calls = append(f.calls, req.GetClusterId())
	if f.errFor != nil {
		if err := f.errFor[req.GetClusterId()]; err != nil {
			return err
		}
	}
	return nil
}

func newTestCommandWithOutput(out *bytes.Buffer) *cobra.Command {
	cmd := &cobra.Command{Use: "test"}
	cmd.SetOut(out)
	cmd.SetErr(out)
	return cmd
}

type fakePublicServiceRegistrar struct {
	reqs []*pb.BootstrapServiceRequest
}

func (f *fakePublicServiceRegistrar) BootstrapService(_ context.Context, req *pb.BootstrapServiceRequest) (*pb.BootstrapServiceResponse, error) {
	f.reqs = append(f.reqs, req)
	return &pb.BootstrapServiceResponse{}, nil
}

type fakeIngressDesiredStateRegistrar struct {
	tlsBundles []*pb.TLSBundle
	sites      []*pb.IngressSite
}

func (f *fakeIngressDesiredStateRegistrar) UpsertTLSBundle(_ context.Context, bundle *pb.TLSBundle) (*pb.TLSBundleResponse, error) {
	f.tlsBundles = append(f.tlsBundles, bundle)
	return &pb.TLSBundleResponse{Bundle: bundle}, nil
}

func (f *fakeIngressDesiredStateRegistrar) UpsertIngressSite(_ context.Context, site *pb.IngressSite) (*pb.IngressSiteResponse, error) {
	f.sites = append(f.sites, site)
	return &pb.IngressSiteResponse{Site: site}, nil
}

func TestReconcileFoghornClusterAssignmentsWithClientAssignsAllManifestClusters(t *testing.T) {
	manifest := &inventory.Manifest{
		Profile: "dev",
		Clusters: map[string]inventory.ClusterConfig{
			"media-central-primary": {},
			"core-central-primary":  {},
		},
	}
	assigner := &fakeFoghornClusterAssigner{}

	var out bytes.Buffer
	if err := reconcileFoghornClusterAssignmentsWithClient(context.Background(), &out, manifest, assigner); err != nil {
		t.Fatalf("reconcile returned error: %v", err)
	}

	if len(assigner.calls) != 2 {
		t.Fatalf("expected 2 assignment calls, got %d", len(assigner.calls))
	}
	if assigner.calls[0] != "core-central-primary" || assigner.calls[1] != "media-central-primary" {
		t.Fatalf("expected sorted cluster assignment order, got %v", assigner.calls)
	}

	output := out.String()
	if !strings.Contains(output, "Reconciling Foghorn cluster assignments") {
		t.Fatalf("expected reconciliation banner in output, got %q", output)
	}
}

func TestReconcileFoghornClusterAssignmentsWithClientReturnsClusterError(t *testing.T) {
	manifest := &inventory.Manifest{
		Profile: "dev",
		Clusters: map[string]inventory.ClusterConfig{
			"core-central-primary":  {},
			"media-central-primary": {},
		},
	}
	assigner := &fakeFoghornClusterAssigner{
		errFor: map[string]error{
			"media-central-primary": errors.New("no running foghorn"),
		},
	}

	err := reconcileFoghornClusterAssignmentsWithClient(context.Background(), &bytes.Buffer{}, manifest, assigner)
	if err == nil {
		t.Fatal("expected reconciliation error")
	}
	if !strings.Contains(err.Error(), "media-central-primary") {
		t.Fatalf("expected cluster id in error, got %v", err)
	}
}

func TestMaybeReconcileBatchFoghornAssignmentsSkipsBatchWithoutFoghorn(t *testing.T) {
	var out bytes.Buffer
	cmd := newTestCommandWithOutput(&out)
	manifest := &inventory.Manifest{
		Profile: "dev",
		Clusters: map[string]inventory.ClusterConfig{
			"core-central-primary": {},
		},
	}
	batch := []*orchestrator.Task{
		{Name: "bridge@core-1", Type: "bridge", ServiceID: "bridge", InstanceID: "core-1", Host: "core-1"},
	}

	if err := maybeReconcileBatchFoghornAssignments(context.Background(), cmd, batch, manifest, map[string]interface{}{}); err != nil {
		t.Fatalf("expected no error for non-foghorn batch, got %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("expected no reconciliation output, got %q", out.String())
	}
}

func TestMaybeReconcileBatchFoghornAssignmentsRequiresQuartermasterRuntimeData(t *testing.T) {
	var out bytes.Buffer
	cmd := newTestCommandWithOutput(&out)
	manifest := &inventory.Manifest{
		Profile: "dev",
		Clusters: map[string]inventory.ClusterConfig{
			"core-central-primary": {},
		},
	}
	batch := []*orchestrator.Task{
		{Name: "foghorn@core-1", Type: "foghorn", ServiceID: "foghorn", InstanceID: "core-1", Host: "core-1"},
	}

	err := maybeReconcileBatchFoghornAssignments(context.Background(), cmd, batch, manifest, map[string]interface{}{})
	if err == nil {
		t.Fatal("expected missing runtime data error")
	}
	if !strings.Contains(err.Error(), "missing Quartermaster connection info") {
		t.Fatalf("expected Quartermaster runtime data error, got %v", err)
	}
}

func TestPublicServiceTypeIncludesLivepeerGateway(t *testing.T) {
	serviceType, ok := publicServiceType("livepeer-gateway")
	if !ok {
		t.Fatal("expected livepeer-gateway to be registered as a public service")
	}
	if serviceType != "livepeer-gateway" {
		t.Fatalf("expected livepeer-gateway service type, got %q", serviceType)
	}
}

func TestPublicServiceTypeIncludesChandler(t *testing.T) {
	serviceType, ok := publicServiceType("chandler")
	if !ok {
		t.Fatal("expected chandler to be registered as a public service")
	}
	if serviceType != "chandler" {
		t.Fatalf("expected chandler service type, got %q", serviceType)
	}
}

func TestAutoIngressDomainsUsesClusterScopedDomainForChandler(t *testing.T) {
	manifest := &inventory.Manifest{
		Profile:    "dev",
		RootDomain: "frameworks.network",
		Clusters: map[string]inventory.ClusterConfig{
			"media-central-primary": {Name: "Media Central Primary"},
		},
	}

	domains, bundleID := autoIngressDomains("chandler", manifest, "media-central-primary")
	if len(domains) != 1 || domains[0] != "chandler.media-central-primary.frameworks.network" {
		t.Fatalf("expected cluster-scoped Chandler ingress domain, got %v", domains)
	}
	if bundleID != "wildcard-media-central-primary-frameworks-network" {
		t.Fatalf("expected cluster wildcard bundle id, got %q", bundleID)
	}
}

func TestDesiredClusterBaseURLPrefersRootDomain(t *testing.T) {
	manifest := &inventory.Manifest{
		Profile:    "dev",
		RootDomain: "frameworks.network",
		Hosts: map[string]inventory.Host{
			"central-eu-1":  {ExternalIP: "10.0.0.10"},
			"regional-eu-1": {ExternalIP: "10.0.0.11"},
		},
		Services: map[string]inventory.ServiceConfig{
			"bridge": {Enabled: true, Host: "regional-eu-1", Port: 18008},
		},
	}

	got := desiredClusterBaseURL(manifest, manifest.Hosts["central-eu-1"], inventory.ServiceConfig{Port: 18002})
	if got != "https://frameworks.network" {
		t.Fatalf("expected root_domain-backed cluster base_url, got %q", got)
	}
}

func TestRegisterIngressDesiredStateWithClientRegistersClusterScopedChandler(t *testing.T) {
	manifest := &inventory.Manifest{
		Profile:    "dev",
		RootDomain: "frameworks.network",
		Hosts: map[string]inventory.Host{
			"central-eu-1": {ExternalIP: "10.0.0.10", Cluster: "media-central-primary"},
		},
		Clusters: map[string]inventory.ClusterConfig{
			"media-central-primary": {Name: "Media Central Primary"},
		},
		Services: map[string]inventory.ServiceConfig{
			"chandler": {Enabled: true, Host: "central-eu-1", Port: 18020},
		},
		Interfaces: map[string]inventory.ServiceConfig{
			"nginx": {Enabled: true, Host: "central-eu-1"},
		},
	}
	task := &orchestrator.Task{
		Name:      "nginx",
		Type:      "nginx",
		ServiceID: "nginx",
		Host:      "central-eu-1",
		ClusterID: "media-central-primary",
	}
	registrar := &fakeIngressDesiredStateRegistrar{}

	if err := registerIngressDesiredStateWithClient(context.Background(), &bytes.Buffer{}, manifest, task, registrar); err != nil {
		t.Fatalf("registerIngressDesiredStateWithClient returned error: %v", err)
	}

	var sawRootWildcard bool
	var sawClusterWildcard bool
	for _, bundle := range registrar.tlsBundles {
		switch bundle.GetBundleId() {
		case "wildcard-frameworks-network":
			sawRootWildcard = true
		case "wildcard-media-central-primary-frameworks-network":
			sawClusterWildcard = true
		}
	}
	if !sawRootWildcard {
		t.Fatal("expected root wildcard bundle to be registered")
	}
	if !sawClusterWildcard {
		t.Fatal("expected cluster wildcard bundle to be registered")
	}

	if len(registrar.sites) != 1 {
		t.Fatalf("expected 1 ingress site, got %d", len(registrar.sites))
	}
	if got := registrar.sites[0].GetDomains(); len(got) != 1 || got[0] != "chandler.media-central-primary.frameworks.network" {
		t.Fatalf("expected cluster-scoped Chandler ingress domain, got %v", got)
	}
	if registrar.sites[0].GetTlsBundleId() != "wildcard-media-central-primary-frameworks-network" {
		t.Fatalf("expected Chandler ingress to use cluster wildcard bundle, got %q", registrar.sites[0].GetTlsBundleId())
	}
}

func TestServiceRegistrationMetadataUsesResolvedGatewayWallet(t *testing.T) {
	envFile := writeTestEnvFile(t, "LIVEPEER_ETH_ACCT_ADDR=0xabc123\n")

	manifest := &inventory.Manifest{
		Profile:    "dev",
		RootDomain: "frameworks.network",
		EnvFiles:   []string{envFile},
		Hosts: map[string]inventory.Host{
			"central-eu-1": {ExternalIP: "10.0.0.10"},
		},
		Clusters: map[string]inventory.ClusterConfig{
			"media-central-primary": {Name: "Media Central Primary"},
		},
		Services: map[string]inventory.ServiceConfig{
			"livepeer-gateway": {
				Enabled: true,
				Host:    "central-eu-1",
			},
		},
	}

	metadata, err := serviceRegistrationMetadata("livepeer-gateway", "central-eu-1", "media-central-primary", manifest, map[string]interface{}{}, "", testLoadSharedEnv(t, manifest), nil)
	if err != nil {
		t.Fatalf("serviceRegistrationMetadata returned error: %v", err)
	}
	if metadata[servicedefs.LivepeerGatewayMetadataWalletAddress] != "0xabc123" {
		t.Fatalf("expected wallet metadata from resolved env, got %v", metadata)
	}
	if metadata[servicedefs.LivepeerGatewayMetadataPublicHost] != "livepeer.media-central-primary.frameworks.network" {
		t.Fatalf("expected cluster-scoped public host, got %q", metadata[servicedefs.LivepeerGatewayMetadataPublicHost])
	}
	if metadata[servicedefs.LivepeerGatewayMetadataPublicPort] != "8935" {
		t.Fatalf("expected public port 8935, got %q", metadata[servicedefs.LivepeerGatewayMetadataPublicPort])
	}
	if metadata[servicedefs.LivepeerGatewayMetadataAdminHost] != "10.0.0.10" {
		t.Fatalf("expected admin host from external IP, got %q", metadata[servicedefs.LivepeerGatewayMetadataAdminHost])
	}
	if metadata[servicedefs.LivepeerGatewayMetadataAdminPort] != "7935" {
		t.Fatalf("expected admin port 7935, got %q", metadata[servicedefs.LivepeerGatewayMetadataAdminPort])
	}
}

func TestExtractInfraCredentialsFromSplitManifestEnvFiles(t *testing.T) {
	baseEnv := writeTestEnvFile(t, "DATABASE_PASSWORD=test-db-pass\n")
	secretsEnv := writeTestEnvFile(t, strings.Join([]string{
		"CLICKHOUSE_PASSWORD=test-ch-pass",
		"CLICKHOUSE_READONLY_PASSWORD=test-ch-ro-pass",
	}, "\n")+"\n")

	manifest := &inventory.Manifest{
		Profile:  "dev",
		EnvFiles: []string{baseEnv, secretsEnv},
	}

	sharedEnv, err := inventory.LoadSharedEnv(manifest, "", "")
	if err != nil {
		t.Fatalf("LoadSharedEnv: %v", err)
	}
	creds := extractInfraCredentials(sharedEnv)
	if got := creds["postgres_password"]; got != "test-db-pass" {
		t.Fatalf("expected postgres_password from first env file, got %v", got)
	}
	if got := creds["clickhouse_password"]; got != "test-ch-pass" {
		t.Fatalf("expected clickhouse_password from second env file, got %v", got)
	}
	if got := creds["clickhouse_readonly_password"]; got != "test-ch-ro-pass" {
		t.Fatalf("expected clickhouse_readonly_password from second env file, got %v", got)
	}
	if _, ok := creds["postgres_user"]; ok {
		t.Fatalf("postgres_user should not be populated from env")
	}
}

func TestBuildServiceEnvVarsLoadsSplitManifestEnvFiles(t *testing.T) {
	baseEnv := writeTestEnvFile(t, strings.Join([]string{
		"ARBITRUM_RPC_ENDPOINT=https://arb.example",
		"LIVEPEER_GATEWAY_HOST=livepeer.frameworks.network",
	}, "\n")+"\n")
	secretsEnv := writeTestEnvFile(t, "LIVEPEER_ETH_ACCT_ADDR=0xabc123\n")

	manifest := &inventory.Manifest{
		Profile:    "dev",
		RootDomain: "frameworks.network",
		EnvFiles:   []string{baseEnv, secretsEnv},
		Clusters: map[string]inventory.ClusterConfig{
			"media-central-primary": {Name: "Media Central Primary"},
		},
		Services: map[string]inventory.ServiceConfig{
			"livepeer-gateway": {
				Enabled: true,
				Config: map[string]string{
					"network": "arbitrum-one-mainnet",
				},
			},
		},
	}

	env, err := buildServiceEnvVars(&orchestrator.Task{
		Name:      "livepeer-gateway",
		Type:      "livepeer-gateway",
		ServiceID: "livepeer-gateway",
		ClusterID: "media-central-primary",
	}, manifest, map[string]interface{}{}, "", "", testLoadSharedEnv(t, manifest))
	if err != nil {
		t.Fatalf("buildServiceEnvVars returned error: %v", err)
	}

	if got := env["eth_url"]; got != "https://arb.example" {
		t.Fatalf("expected eth_url from first env file, got %q", got)
	}
	if got := env["eth_acct_addr"]; got != "0xabc123" {
		t.Fatalf("expected eth_acct_addr from second env file, got %q", got)
	}
	if got := env["gateway_host"]; got != "livepeer.media-central-primary.frameworks.network" {
		t.Fatalf("expected cluster-scoped gateway_host, got %q", got)
	}
}

func TestBuildServiceEnvVarsDerivesSharedRuntimeValues(t *testing.T) {
	baseEnv := writeTestEnvFile(t, strings.Join([]string{
		"FROM_EMAIL=info@frameworks.network",
		"X402_GAS_WALLET_ADDRESS=0xabc123",
	}, "\n")+"\n")

	manifest := &inventory.Manifest{
		Profile:    "dev",
		RootDomain: "frameworks.network",
		EnvFiles:   []string{baseEnv},
		Services: map[string]inventory.ServiceConfig{
			"foghorn": {Enabled: true},
		},
	}

	env, err := buildServiceEnvVars(&orchestrator.Task{
		Name:      "foghorn",
		Type:      "foghorn",
		ServiceID: "foghorn",
	}, manifest, map[string]interface{}{}, "", "", testLoadSharedEnv(t, manifest))
	if err != nil {
		t.Fatalf("buildServiceEnvVars returned error: %v", err)
	}

	if got := env["BRAND_DOMAIN"]; got != "frameworks.network" {
		t.Fatalf("expected BRAND_DOMAIN from manifest root_domain, got %q", got)
	}
	if got := env["FROM_EMAIL"]; got != "info@frameworks.network" {
		t.Fatalf("expected FROM_EMAIL from env files, got %q", got)
	}
	if got := env["X402_GAS_WALLET_ADDRESS"]; got != "0xabc123" {
		t.Fatalf("expected X402_GAS_WALLET_ADDRESS from env files, got %q", got)
	}
	if got := env["DATABASE_USER"]; got != "foghorn" {
		t.Fatalf("expected DATABASE_USER to default to service name, got %q", got)
	}
}

func TestBuildServiceEnvVarsDerivesRegionFromHostLabels(t *testing.T) {
	manifest := &inventory.Manifest{
		Profile: "dev",
		Hosts: map[string]inventory.Host{
			"regional-us-1": {
				Labels: map[string]string{
					"region": "us-east",
				},
			},
		},
		Services: map[string]inventory.ServiceConfig{
			"foghorn": {Enabled: true, Host: "regional-us-1"},
		},
	}

	env, err := buildServiceEnvVars(&orchestrator.Task{
		Name:      "foghorn",
		Type:      "foghorn",
		ServiceID: "foghorn",
		Host:      "regional-us-1",
	}, manifest, map[string]interface{}{}, "", "", testLoadSharedEnv(t, manifest))
	if err != nil {
		t.Fatalf("buildServiceEnvVars returned error: %v", err)
	}

	if got := env["REGION"]; got != "us-east" {
		t.Fatalf("expected REGION from host labels, got %q", got)
	}
}

func TestValidateInternalGRPCTLSCoverageRejectsHostWithoutPrivateer(t *testing.T) {
	manifest := &inventory.Manifest{
		Profile: "dev",
		Hosts: map[string]inventory.Host{
			"core-1": {ExternalIP: "10.0.0.1", Roles: []string{"control"}},
			"core-2": {ExternalIP: "10.0.0.2", Roles: []string{"control"}},
		},
		Services: map[string]inventory.ServiceConfig{
			"navigator":     {Enabled: true, Host: "core-1"},
			"quartermaster": {Enabled: true, Host: "core-2"},
			"privateer":     {Enabled: true, Host: "core-1"},
		},
	}

	err := validateInternalGRPCTLSCoverage(manifest)
	if err == nil {
		t.Fatal("expected internal gRPC TLS coverage validation to fail")
	}
	if !strings.Contains(err.Error(), "core-2") {
		t.Fatalf("expected uncovered host in error, got %v", err)
	}
}

func TestBuildServiceEnvVarsProductionForcesSecureDefaults(t *testing.T) {
	envFile := writeTestEnvFile(t, testSharedSecrets+strings.Join([]string{
		"NAVIGATOR_INTERNAL_CA_ROOT_CERT_FILE=/etc/frameworks/ca/root.crt",
		"NAVIGATOR_INTERNAL_CA_INTERMEDIATE_CERT_FILE=/etc/frameworks/ca/intermediate.crt",
		"NAVIGATOR_INTERNAL_CA_INTERMEDIATE_KEY_FILE=/etc/frameworks/ca/intermediate.key",
	}, "\n")+"\n")

	manifest := &inventory.Manifest{
		Profile:    "production",
		RootDomain: "frameworks.network",
		EnvFiles:   []string{envFile},
		Hosts: map[string]inventory.Host{
			"core-1": {ExternalIP: "10.0.0.1", Roles: []string{"control"}},
		},
		Services: map[string]inventory.ServiceConfig{
			"navigator": {Enabled: true, Host: "core-1"},
			"foghorn":   {Enabled: true, Host: "core-1"},
		},
	}
	task := &orchestrator.Task{
		Name:      "foghorn",
		Type:      "foghorn",
		ServiceID: "foghorn",
		Host:      "core-1",
		ClusterID: "cluster-a",
		Phase:     orchestrator.PhaseApplications,
	}

	env, err := buildServiceEnvVars(task, manifest, map[string]interface{}{}, "", "", testLoadSharedEnv(t, manifest))
	if err != nil {
		t.Fatalf("buildServiceEnvVars returned error: %v", err)
	}
	if env["BUILD_ENV"] != "production" {
		t.Fatalf("expected BUILD_ENV=production, got %q", env["BUILD_ENV"])
	}
	if env["GRPC_ALLOW_INSECURE"] != "false" {
		t.Fatalf("expected GRPC_ALLOW_INSECURE=false, got %q", env["GRPC_ALLOW_INSECURE"])
	}
	if _, ok := env["NODE_ENV"]; ok {
		t.Fatalf("expected NODE_ENV to be absent from service env, got %q", env["NODE_ENV"])
	}
	if _, ok := env["DECKLOG_USE_TLS"]; ok {
		t.Fatalf("expected DECKLOG_USE_TLS to be absent from service env, got %q", env["DECKLOG_USE_TLS"])
	}
}

func TestBuildServiceEnvVarsProductionRequiresNavigatorManagedCA(t *testing.T) {
	envFile := writeTestEnvFile(t, testSharedSecrets)
	manifest := &inventory.Manifest{
		Profile:    "production",
		RootDomain: "frameworks.network",
		EnvFiles:   []string{envFile},
		Hosts: map[string]inventory.Host{
			"core-1": {ExternalIP: "10.0.0.1", Roles: []string{"control"}},
		},
		Services: map[string]inventory.ServiceConfig{
			"navigator": {Enabled: true, Host: "core-1"},
		},
	}
	task := &orchestrator.Task{
		Name:      "navigator",
		Type:      "navigator",
		ServiceID: "navigator",
		Host:      "core-1",
		ClusterID: "cluster-a",
		Phase:     orchestrator.PhaseApplications,
	}

	_, err := buildServiceEnvVars(task, manifest, map[string]interface{}{}, "", "", testLoadSharedEnv(t, manifest))
	if err == nil {
		t.Fatal("expected managed CA env validation to fail")
	}
	if !strings.Contains(err.Error(), "NAVIGATOR_INTERNAL_CA_ROOT_CERT_FILE") {
		t.Fatalf("expected missing CA env vars in error, got %v", err)
	}
}

func TestBuildServiceEnvVarsProductionAcceptsNavigatorManagedCABase64Env(t *testing.T) {
	envFile := writeTestEnvFile(t, testSharedSecrets+strings.Join([]string{
		"NAVIGATOR_INTERNAL_CA_ROOT_CERT_PEM_B64=cm9vdA==",
		"NAVIGATOR_INTERNAL_CA_INTERMEDIATE_CERT_PEM_B64=aW50ZXJtZWRpYXRl",
		"NAVIGATOR_INTERNAL_CA_INTERMEDIATE_KEY_PEM_B64=a2V5",
	}, "\n")+"\n")

	manifest := &inventory.Manifest{
		Profile:    "production",
		RootDomain: "frameworks.network",
		EnvFiles:   []string{envFile},
		Hosts: map[string]inventory.Host{
			"core-1": {ExternalIP: "10.0.0.1", Roles: []string{"control"}},
		},
		Services: map[string]inventory.ServiceConfig{
			"navigator": {Enabled: true, Host: "core-1"},
		},
	}
	task := &orchestrator.Task{
		Name:      "navigator",
		Type:      "navigator",
		ServiceID: "navigator",
		Host:      "core-1",
		ClusterID: "cluster-a",
		Phase:     orchestrator.PhaseApplications,
	}

	if _, err := buildServiceEnvVars(task, manifest, map[string]interface{}{}, "", "", testLoadSharedEnv(t, manifest)); err != nil {
		t.Fatalf("expected base64 CA envs to satisfy prod validation, got %v", err)
	}
}

func TestBuildServiceEnvVarsUsesMeshHostsForBackendDependencies(t *testing.T) {
	manifest := &inventory.Manifest{
		Profile:    "dev",
		RootDomain: "frameworks.network",
		Hosts: map[string]inventory.Host{
			"central-eu-1":  {ExternalIP: "10.0.0.10", Roles: []string{"control"}},
			"regional-eu-1": {ExternalIP: "10.0.0.11", Roles: []string{"services"}},
			"yuga-eu-1":     {ExternalIP: "10.0.0.12", Roles: []string{"infrastructure"}},
		},
		Infrastructure: inventory.InfrastructureConfig{
			Postgres: &inventory.PostgresConfig{
				Enabled: true,
				Engine:  "yugabyte",
				Port:    5433,
				Nodes: []inventory.PostgresNode{
					{Host: "yuga-eu-1", ID: 1},
				},
			},
			ClickHouse: &inventory.ClickHouseConfig{
				Enabled: true,
				Host:    "yuga-eu-1",
				Port:    9000,
			},
			Kafka: &inventory.KafkaConfig{
				Enabled: true,
				Brokers: []inventory.KafkaBroker{
					{Host: "central-eu-1", ID: 1, Port: 9092},
					{Host: "regional-eu-1", ID: 2, Port: 9093},
				},
			},
			Redis: &inventory.RedisConfig{
				Enabled: true,
				Instances: []inventory.RedisInstance{
					{Name: "foghorn", Host: "central-eu-1", Port: 6379},
					{Name: "chatwoot", Host: "central-eu-1", Port: 6380},
				},
			},
		},
		Services: map[string]inventory.ServiceConfig{
			"foghorn":  {Enabled: true, Host: "central-eu-1"},
			"chandler": {Enabled: true, Host: "central-eu-1", Port: 18020},
			"listmonk": {Enabled: true, Host: "central-eu-1", Port: 9001},
			"chatwoot": {Enabled: true, Host: "central-eu-1", Port: 18092},
		},
	}
	task := &orchestrator.Task{
		Name:      "foghorn",
		Type:      "foghorn",
		ServiceID: "foghorn",
		Host:      "central-eu-1",
		ClusterID: "cluster-a",
		Phase:     orchestrator.PhaseApplications,
	}

	env, err := buildServiceEnvVars(task, manifest, map[string]interface{}{}, "", "", testLoadSharedEnv(t, manifest))
	if err != nil {
		t.Fatalf("buildServiceEnvVars returned error: %v", err)
	}

	if env["DATABASE_HOST"] != "yuga-eu-1.internal" {
		t.Fatalf("expected DATABASE_HOST to use mesh host, got %q", env["DATABASE_HOST"])
	}
	if env["DATABASE_URL"] != "postgres://foghorn@yuga-eu-1.internal:5433/foghorn?sslmode=disable" {
		t.Fatalf("expected DATABASE_URL to use mesh host with service-level user and database, got %q", env["DATABASE_URL"])
	}
	if env["KAFKA_BROKERS"] != "central-eu-1.internal:9092,regional-eu-1.internal:9093" {
		t.Fatalf("expected KAFKA_BROKERS to use mesh hosts, got %q", env["KAFKA_BROKERS"])
	}
	if env["CLICKHOUSE_ADDR"] != "yuga-eu-1.internal:9000" {
		t.Fatalf("expected CLICKHOUSE_ADDR to use mesh host, got %q", env["CLICKHOUSE_ADDR"])
	}
	if env["REDIS_FOGHORN_ADDR"] != "central-eu-1.internal:6379" {
		t.Fatalf("expected REDIS_FOGHORN_ADDR to use mesh host, got %q", env["REDIS_FOGHORN_ADDR"])
	}
	if env["REDIS_CHATWOOT_ADDR"] != "central-eu-1.internal:6380" {
		t.Fatalf("expected REDIS_CHATWOOT_ADDR to use mesh host, got %q", env["REDIS_CHATWOOT_ADDR"])
	}
	if _, ok := env["CHANDLER_HOST"]; ok {
		t.Fatalf("expected CHANDLER_HOST not to be auto-generated as an internal dependency, got %q", env["CHANDLER_HOST"])
	}
	if env["LISTMONK_URL"] != "http://central-eu-1.internal:9001" {
		t.Fatalf("expected LISTMONK_URL to use mesh host, got %q", env["LISTMONK_URL"])
	}
	if env["CHATWOOT_HOST"] != "central-eu-1.internal" {
		t.Fatalf("expected CHATWOOT_HOST to use mesh host, got %q", env["CHATWOOT_HOST"])
	}
}

func TestBuildTaskConfigKafkaUsesMeshControllerQuorumAddresses(t *testing.T) {
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{
			"central-eu-1":      {ExternalIP: "136.144.189.92", WireguardIP: "10.88.0.10", Roles: []string{"control"}},
			"regional-eu-1":     {ExternalIP: "91.99.189.88", WireguardIP: "10.88.0.11", Roles: []string{"data"}},
			"frameworks-us-ctl": {ExternalIP: "5.161.86.203", WireguardIP: "10.88.0.12", Roles: []string{"data"}},
		},
		Infrastructure: inventory.InfrastructureConfig{
			Kafka: &inventory.KafkaConfig{
				Enabled: true,
				Version: "4.2.0",
				Controllers: []inventory.KafkaController{
					{Host: "central-eu-1", ID: 100, Port: 9093, DirID: "dir-a"},
					{Host: "regional-eu-1", ID: 101, Port: 9093, DirID: "dir-b"},
					{Host: "frameworks-us-ctl", ID: 102, Port: 9093, DirID: "dir-c"},
				},
				Brokers: []inventory.KafkaBroker{
					{Host: "central-eu-1", ID: 1, Port: 9092},
					{Host: "regional-eu-1", ID: 2, Port: 9092},
					{Host: "frameworks-us-ctl", ID: 3, Port: 9092},
				},
			},
		},
		Services: map[string]inventory.ServiceConfig{
			"privateer": {Enabled: true},
		},
	}
	task := &orchestrator.Task{
		Name:       "kafka-broker-2",
		Type:       "kafka",
		ServiceID:  "kafka",
		InstanceID: "2",
		Host:       "regional-eu-1",
		Phase:      orchestrator.PhaseInfrastructure,
	}

	config, err := buildTaskConfig(task, manifest, map[string]interface{}{}, false, "", map[string]string{}, nil)
	if err != nil {
		t.Fatalf("buildTaskConfig returned error: %v", err)
	}

	got, _ := config.Metadata["controller_quorum_voters"].(string)
	want := "100@10.88.0.10:9093,101@10.88.0.11:9093,102@10.88.0.12:9093"
	if got != want {
		t.Fatalf("expected controller_quorum_voters %q, got %q", want, got)
	}

	controllers, ok := config.Metadata["controllers"].([]map[string]any)
	if !ok || len(controllers) != 3 {
		t.Fatalf("expected 3 controller metadata entries, got %#v", config.Metadata["controllers"])
	}
	if host, _ := controllers[2]["host"].(string); host != "10.88.0.12" {
		t.Fatalf("expected third controller host to use mesh IP, got %q", host)
	}
}

func TestRegisterPublicServiceInstanceWithClientUsesResolvedGatewayMetadata(t *testing.T) {
	envFile := writeTestEnvFile(t, "LIVEPEER_ETH_ACCT_ADDR=0xabc123\n")
	manifest := &inventory.Manifest{
		Profile:    "dev",
		RootDomain: "frameworks.network",
		EnvFiles:   []string{envFile},
		Hosts: map[string]inventory.Host{
			"core-1": {
				ExternalIP: "10.0.0.10",
				Roles:      []string{"core"},
			},
		},
		Clusters: map[string]inventory.ClusterConfig{
			"media-a": {},
		},
		Services: map[string]inventory.ServiceConfig{
			"livepeer-gateway": {
				Enabled: true,
				Host:    "core-1",
				Port:    8935,
			},
		},
	}
	task := &orchestrator.Task{
		Name:      "livepeer-gateway",
		Type:      "livepeer-gateway",
		ServiceID: "livepeer-gateway",
		Host:      "core-1",
		ClusterID: "media-a",
		Phase:     orchestrator.PhaseApplications,
	}
	runtimeData := map[string]interface{}{
		"service_token": "svc-token",
	}
	registrar := &fakePublicServiceRegistrar{}

	var out bytes.Buffer
	if err := registerPublicServiceInstanceWithClient(context.Background(), &out, manifest, task, runtimeData, "", testLoadSharedEnv(t, manifest), nil, registrar); err != nil {
		t.Fatalf("registerPublicServiceInstanceWithClient returned error: %v", err)
	}
	if len(registrar.reqs) != 1 {
		t.Fatalf("expected one registration request, got %d", len(registrar.reqs))
	}
	if got := registrar.reqs[0].GetHealthEndpoint(); got != "/healthz" {
		t.Fatalf("expected /healthz health endpoint, got %q", got)
	}
	if got := registrar.reqs[0].GetMetadata()[servicedefs.LivepeerGatewayMetadataWalletAddress]; got != "0xabc123" {
		t.Fatalf("expected wallet metadata, got %q", got)
	}
	if got := registrar.reqs[0].GetMetadata()[servicedefs.LivepeerGatewayMetadataPublicHost]; got != "livepeer.media-a.frameworks.network" {
		t.Fatalf("expected cluster-scoped public host, got %q", got)
	}
	if got := registrar.reqs[0].GetMetadata()[servicedefs.LivepeerGatewayMetadataAdminPort]; got != "7935" {
		t.Fatalf("expected admin port metadata, got %q", got)
	}
}

func TestBuildTaskConfigSetsObservabilityComponent(t *testing.T) {
	manifest := &inventory.Manifest{
		Profile: "dev",
		Hosts: map[string]inventory.Host{
			"core-1": {ExternalIP: "10.0.0.10"},
		},
		Observability: map[string]inventory.ServiceConfig{
			"vmagent": {
				Enabled: true,
				Mode:    "native",
				Host:    "core-1",
			},
		},
	}
	cfg, err := buildTaskConfig(&orchestrator.Task{
		Name:      "vmagent",
		Type:      "vmagent",
		ServiceID: "vmagent",
		Host:      "core-1",
		Phase:     orchestrator.PhaseInterfaces,
	}, manifest, map[string]interface{}{}, false, "", map[string]string{}, nil)
	if err != nil {
		t.Fatalf("buildTaskConfig returned error: %v", err)
	}
	if got := cfg.Metadata["component"]; got != "vmagent" {
		t.Fatalf("component = %v, want vmagent", got)
	}
	if got := cfg.Metadata["service_name"]; got != "vmagent" {
		t.Fatalf("service_name = %v, want vmagent", got)
	}
}

func TestBuildTaskConfigBuildsProxySitesForReverseProxy(t *testing.T) {
	manifest := &inventory.Manifest{
		Profile:    "dev",
		RootDomain: "frameworks.network",
		Hosts: map[string]inventory.Host{
			"edge-1": {ExternalIP: "10.0.0.10", Cluster: "media-a"},
		},
		Clusters: map[string]inventory.ClusterConfig{
			"media-a": {},
		},
		Services: map[string]inventory.ServiceConfig{
			"chartroom": {Enabled: true, Host: "edge-1", Port: 18030},
		},
		Interfaces: map[string]inventory.ServiceConfig{
			"caddy": {Enabled: true, Host: "edge-1", Mode: "native"},
		},
	}
	cfg, err := buildTaskConfig(&orchestrator.Task{
		Name:      "caddy",
		Type:      "caddy",
		ServiceID: "caddy",
		Host:      "edge-1",
		ClusterID: "media-a",
		Phase:     orchestrator.PhaseInterfaces,
	}, manifest, map[string]interface{}{}, false, "", map[string]string{}, nil)
	if err != nil {
		t.Fatalf("buildTaskConfig returned error: %v", err)
	}
	sites, ok := cfg.Metadata["proxy_sites"].([]map[string]any)
	if !ok || len(sites) == 0 {
		t.Fatalf("sites missing or wrong type: %#v", cfg.Metadata["sites"])
	}
	if got := sites[0]["upstream"]; got != "127.0.0.1:18030" {
		t.Fatalf("upstream = %v", got)
	}
	domains, ok := sites[0]["domains"].([]string)
	if !ok || len(domains) == 0 {
		t.Fatalf("domains missing or wrong type: %#v", sites[0]["domains"])
	}
}

func TestBuildTaskConfigAllowsNativeNginxProxySites(t *testing.T) {
	manifest := &inventory.Manifest{
		Profile:    "dev",
		RootDomain: "frameworks.network",
		Hosts: map[string]inventory.Host{
			"edge-1": {ExternalIP: "10.0.0.10", Cluster: "media-a"},
		},
		Clusters: map[string]inventory.ClusterConfig{
			"media-a": {},
		},
		Services: map[string]inventory.ServiceConfig{
			"bridge": {Enabled: true, Host: "edge-1", Port: 18000},
		},
		Interfaces: map[string]inventory.ServiceConfig{
			"nginx": {Enabled: true, Host: "edge-1", Mode: "native"},
		},
	}
	cfg, err := buildTaskConfig(&orchestrator.Task{
		Name:      "nginx",
		Type:      "nginx",
		ServiceID: "nginx",
		Host:      "edge-1",
		ClusterID: "media-a",
		Phase:     orchestrator.PhaseInterfaces,
	}, manifest, map[string]interface{}{}, false, "", map[string]string{}, nil)
	if err != nil {
		t.Fatalf("buildTaskConfig returned error: %v", err)
	}
	if cfg.Mode != "native" {
		t.Fatalf("mode = %q, want native", cfg.Mode)
	}
	sites, ok := cfg.Metadata["proxy_sites"].([]map[string]any)
	if !ok || len(sites) == 0 {
		t.Fatalf("proxy_sites missing or wrong type: %#v", cfg.Metadata["proxy_sites"])
	}
	if got := sites[0]["upstream"]; got != "127.0.0.1:18000" {
		t.Fatalf("upstream = %v", got)
	}
}

func TestBuildTaskConfigIncludesExplicitIngressSitesAndTLSMetadata(t *testing.T) {
	manifest := &inventory.Manifest{
		Profile:    "production",
		RootDomain: "frameworks.network",
		Hosts: map[string]inventory.Host{
			"edge-1": {ExternalIP: "10.0.0.10", Cluster: "media-a"},
		},
		Clusters: map[string]inventory.ClusterConfig{
			"media-a": {},
		},
		Interfaces: map[string]inventory.ServiceConfig{
			"nginx": {Enabled: true, Host: "edge-1", Mode: "native"},
		},
		TLSBundles: map[string]inventory.TLSBundleConfig{
			"bridge-cert": {Metadata: map[string]string{
				"tls_cert_path": "/etc/frameworks/certs/bridge.crt",
				"tls_key_path":  "/etc/frameworks/certs/bridge.key",
			}},
		},
		IngressSites: map[string]inventory.IngressSiteConfig{
			"bridge-graphql": {
				Node:        "edge-1",
				Domains:     []string{"bridge.frameworks.network"},
				TLSBundleID: "bridge-cert",
				Kind:        "reverse_proxy_http",
				Upstream:    "127.0.0.1:18000",
				Metadata: map[string]string{
					"path_prefix": "/graphql",
				},
			},
		},
	}
	cfg, err := buildTaskConfig(&orchestrator.Task{
		Name:      "nginx",
		Type:      "nginx",
		ServiceID: "nginx",
		Host:      "edge-1",
		ClusterID: "media-a",
		Phase:     orchestrator.PhaseInterfaces,
	}, manifest, map[string]interface{}{}, false, "", map[string]string{}, nil)
	if err != nil {
		t.Fatalf("buildTaskConfig returned error: %v", err)
	}
	sites, ok := cfg.Metadata["proxy_sites"].([]map[string]any)
	if !ok || len(sites) != 1 {
		t.Fatalf("proxy_sites missing or wrong type: %#v", cfg.Metadata["proxy_sites"])
	}
	site := sites[0]
	if got := site["tls_cert_path"]; got != "/etc/frameworks/certs/bridge.crt" {
		t.Fatalf("tls_cert_path = %v", got)
	}
	if got := site["tls_key_path"]; got != "/etc/frameworks/certs/bridge.key" {
		t.Fatalf("tls_key_path = %v", got)
	}
	if got := site["path_prefix"]; got != "/graphql" {
		t.Fatalf("path_prefix = %v", got)
	}
}

func TestBuildTaskConfigDedupesProxySites(t *testing.T) {
	manifest := &inventory.Manifest{
		Profile:    "production",
		RootDomain: "frameworks.network",
		Hosts: map[string]inventory.Host{
			"edge-1": {ExternalIP: "10.0.0.10", Cluster: "media-a"},
		},
		Clusters: map[string]inventory.ClusterConfig{
			"media-a": {Name: "Media A"},
		},
		Observability: map[string]inventory.ServiceConfig{
			"vmauth": {Enabled: true, Host: "edge-1", Port: 8427},
		},
		Interfaces: map[string]inventory.ServiceConfig{
			"nginx": {Enabled: true, Host: "edge-1", Mode: "native"},
		},
	}
	cfg, err := buildTaskConfig(&orchestrator.Task{
		Name:      "nginx",
		Type:      "nginx",
		ServiceID: "nginx",
		Host:      "edge-1",
		ClusterID: "media-a",
		Phase:     orchestrator.PhaseInterfaces,
	}, manifest, map[string]interface{}{}, false, "", map[string]string{}, nil)
	if err != nil {
		t.Fatalf("buildTaskConfig returned error: %v", err)
	}
	sites, ok := cfg.Metadata["proxy_sites"].([]map[string]any)
	if !ok {
		t.Fatalf("proxy_sites missing or wrong type: %#v", cfg.Metadata["proxy_sites"])
	}
	if len(sites) != 1 {
		t.Fatalf("proxy_sites len = %d, want 1: %#v", len(sites), sites)
	}
}

func TestValidateGatewayMeshCoverageRejectsGatewayOutsidePrivateerHosts(t *testing.T) {
	manifest := &inventory.Manifest{
		Profile: "dev",
		Hosts: map[string]inventory.Host{
			"core-1": {ExternalIP: "10.0.0.10", Roles: []string{"control"}},
			"core-2": {ExternalIP: "10.0.0.11", Roles: []string{"control"}},
		},
		Services: map[string]inventory.ServiceConfig{
			"privateer": {
				Enabled: true,
				Host:    "core-1",
			},
			"livepeer-gateway": {
				Enabled: true,
				Host:    "core-2",
			},
		},
	}

	err := validateGatewayMeshCoverage(manifest)
	if err == nil {
		t.Fatal("expected mesh coverage validation error")
	}
	if !strings.Contains(err.Error(), "core-2") {
		t.Fatalf("expected gateway host in error, got %v", err)
	}
}

func TestValidateGatewayMeshCoverageAllowsGatewayOnPrivateerHost(t *testing.T) {
	manifest := &inventory.Manifest{
		Profile: "dev",
		Hosts: map[string]inventory.Host{
			"core-1": {ExternalIP: "10.0.0.10", Roles: []string{"control"}},
		},
		Services: map[string]inventory.ServiceConfig{
			"privateer": {
				Enabled: true,
			},
			"livepeer-gateway": {
				Enabled: true,
				Host:    "core-1",
			},
		},
	}

	if err := validateGatewayMeshCoverage(manifest); err != nil {
		t.Fatalf("expected mesh coverage validation to pass, got %v", err)
	}
}

func TestPhaseRequiresGatewayMeshValidation(t *testing.T) {
	tests := []struct {
		name  string
		phase orchestrator.Phase
		want  bool
	}{
		{name: "infrastructure", phase: orchestrator.PhaseInfrastructure, want: false},
		{name: "applications", phase: orchestrator.PhaseApplications, want: true},
		{name: "interfaces", phase: orchestrator.PhaseInterfaces, want: false},
		{name: "all", phase: orchestrator.PhaseAll, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := phaseRequiresGatewayMeshValidation(tt.phase); got != tt.want {
				t.Fatalf("phaseRequiresGatewayMeshValidation(%v) = %v, want %v", tt.phase, got, tt.want)
			}
		})
	}
}

func TestQuartermasterMeshGRPCAddrUsesMeshIP(t *testing.T) {
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{
			"core-1": {ExternalIP: "203.0.113.5", WireguardIP: "10.88.0.2"},
		},
		Services: map[string]inventory.ServiceConfig{
			"quartermaster": {Enabled: true, Host: "core-1", GRPCPort: 19002},
		},
	}
	got := quartermasterMeshGRPCAddr(manifest)
	if got != "10.88.0.2:19002" {
		t.Fatalf("quartermasterMeshGRPCAddr = %q, want 10.88.0.2:19002", got)
	}
}

func TestQuartermasterMeshGRPCAddrDefaultPort(t *testing.T) {
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{
			"core-1": {ExternalIP: "203.0.113.5", WireguardIP: "10.88.0.2"},
		},
		Services: map[string]inventory.ServiceConfig{
			"quartermaster": {Enabled: true, Host: "core-1"},
		},
	}
	got := quartermasterMeshGRPCAddr(manifest)
	if got != "10.88.0.2:19002" {
		t.Fatalf("quartermasterMeshGRPCAddr = %q, want 10.88.0.2:19002", got)
	}
}

func TestQuartermasterMeshGRPCAddrMissingService(t *testing.T) {
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{"core-1": {ExternalIP: "203.0.113.5", WireguardIP: "10.88.0.2"}},
	}
	if got := quartermasterMeshGRPCAddr(manifest); got != "" {
		t.Fatalf("quartermasterMeshGRPCAddr = %q, want empty", got)
	}
}
