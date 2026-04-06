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

type fakeBootstrapTokenCreator struct {
	token string
	reqs  []*pb.CreateBootstrapTokenRequest
}

func (f *fakeBootstrapTokenCreator) CreateBootstrapToken(_ context.Context, req *pb.CreateBootstrapTokenRequest) (*pb.CreateBootstrapTokenResponse, error) {
	f.reqs = append(f.reqs, req)
	return &pb.CreateBootstrapTokenResponse{
		Token: &pb.BootstrapToken{Token: f.token},
	}, nil
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
		Clusters: map[string]inventory.ClusterConfig{
			"core-central-primary": {},
		},
	}
	batch := []*orchestrator.Task{
		{Name: "bridge@core-1", Type: "bridge", Host: "core-1"},
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
		Clusters: map[string]inventory.ClusterConfig{
			"core-central-primary": {},
		},
	}
	batch := []*orchestrator.Task{
		{Name: "foghorn@core-1", Type: "foghorn", Host: "core-1"},
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
	if got != "frameworks.network" {
		t.Fatalf("expected root_domain-backed cluster base_url, got %q", got)
	}
}

func TestRegisterIngressDesiredStateWithClientRegistersClusterScopedChandler(t *testing.T) {
	manifest := &inventory.Manifest{
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
		Host:      "central-eu-1",
		ClusterID: "media-central-primary",
	}
	registrar := &fakeIngressDesiredStateRegistrar{}

	if err := registerIngressDesiredStateWithClient(context.Background(), &bytes.Buffer{}, manifest, task, manifest.Hosts["central-eu-1"], registrar); err != nil {
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
		RootDomain: "frameworks.network",
		EnvFile:    envFile,
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

	metadata, err := serviceRegistrationMetadata("livepeer-gateway", "central-eu-1", "media-central-primary", manifest, map[string]interface{}{}, "")
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

func TestEnsurePrivateerEnrollmentTokenWithClientStoresClusterToken(t *testing.T) {
	runtimeData := map[string]interface{}{}
	creator := &fakeBootstrapTokenCreator{token: "bt_test"}

	if err := ensurePrivateerEnrollmentTokenWithClient(context.Background(), runtimeData, "tenant-1", "cluster-a", creator); err != nil {
		t.Fatalf("ensurePrivateerEnrollmentTokenWithClient returned error: %v", err)
	}

	tokens, ok := runtimeData["enrollment_tokens"].(map[string]string)
	if !ok {
		t.Fatalf("expected enrollment_tokens map, got %T", runtimeData["enrollment_tokens"])
	}
	if tokens["cluster-a"] != "bt_test" {
		t.Fatalf("expected stored token, got %q", tokens["cluster-a"])
	}
	if len(creator.reqs) != 1 || creator.reqs[0].GetClusterId() != "cluster-a" {
		t.Fatalf("expected token creation for cluster-a, got %+v", creator.reqs)
	}
}

func TestValidateInternalGRPCTLSCoverageRejectsHostWithoutPrivateer(t *testing.T) {
	manifest := &inventory.Manifest{
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
	envFile := writeTestEnvFile(t, strings.Join([]string{
		"NAVIGATOR_INTERNAL_CA_ROOT_CERT_FILE=/etc/frameworks/ca/root.crt",
		"NAVIGATOR_INTERNAL_CA_INTERMEDIATE_CERT_FILE=/etc/frameworks/ca/intermediate.crt",
		"NAVIGATOR_INTERNAL_CA_INTERMEDIATE_KEY_FILE=/etc/frameworks/ca/intermediate.key",
	}, "\n")+"\n")

	manifest := &inventory.Manifest{
		Profile:    "production",
		RootDomain: "frameworks.network",
		EnvFile:    envFile,
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
		Host:      "core-1",
		ClusterID: "cluster-a",
		Phase:     orchestrator.PhaseApplications,
	}

	env, err := buildServiceEnvVars(task, manifest, map[string]interface{}{}, "", "")
	if err != nil {
		t.Fatalf("buildServiceEnvVars returned error: %v", err)
	}
	if env["NODE_ENV"] != "production" {
		t.Fatalf("expected NODE_ENV=production, got %q", env["NODE_ENV"])
	}
	if env["BUILD_ENV"] != "production" {
		t.Fatalf("expected BUILD_ENV=production, got %q", env["BUILD_ENV"])
	}
	if env["GRPC_ALLOW_INSECURE"] != "false" {
		t.Fatalf("expected GRPC_ALLOW_INSECURE=false, got %q", env["GRPC_ALLOW_INSECURE"])
	}
	if env["DECKLOG_USE_TLS"] != "true" {
		t.Fatalf("expected DECKLOG_USE_TLS=true, got %q", env["DECKLOG_USE_TLS"])
	}
}

func TestBuildServiceEnvVarsProductionRequiresNavigatorManagedCA(t *testing.T) {
	manifest := &inventory.Manifest{
		Profile:    "production",
		RootDomain: "frameworks.network",
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
		Host:      "core-1",
		ClusterID: "cluster-a",
		Phase:     orchestrator.PhaseApplications,
	}

	_, err := buildServiceEnvVars(task, manifest, map[string]interface{}{}, "", "")
	if err == nil {
		t.Fatal("expected managed CA env validation to fail")
	}
	if !strings.Contains(err.Error(), "NAVIGATOR_INTERNAL_CA_ROOT_CERT_FILE") {
		t.Fatalf("expected missing CA env vars in error, got %v", err)
	}
}

func TestBuildServiceEnvVarsProductionAcceptsNavigatorManagedCABase64Env(t *testing.T) {
	envFile := writeTestEnvFile(t, strings.Join([]string{
		"NAVIGATOR_INTERNAL_CA_ROOT_CERT_PEM_B64=cm9vdA==",
		"NAVIGATOR_INTERNAL_CA_INTERMEDIATE_CERT_PEM_B64=aW50ZXJtZWRpYXRl",
		"NAVIGATOR_INTERNAL_CA_INTERMEDIATE_KEY_PEM_B64=a2V5",
	}, "\n")+"\n")

	manifest := &inventory.Manifest{
		Profile:    "production",
		RootDomain: "frameworks.network",
		EnvFile:    envFile,
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
		Host:      "core-1",
		ClusterID: "cluster-a",
		Phase:     orchestrator.PhaseApplications,
	}

	if _, err := buildServiceEnvVars(task, manifest, map[string]interface{}{}, "", ""); err != nil {
		t.Fatalf("expected base64 CA envs to satisfy prod validation, got %v", err)
	}
}

func TestBuildServiceEnvVarsUsesMeshHostsForBackendDependencies(t *testing.T) {
	manifest := &inventory.Manifest{
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
		Host:      "central-eu-1",
		ClusterID: "cluster-a",
		Phase:     orchestrator.PhaseApplications,
	}

	env, err := buildServiceEnvVars(task, manifest, map[string]interface{}{}, "", "")
	if err != nil {
		t.Fatalf("buildServiceEnvVars returned error: %v", err)
	}

	if env["DATABASE_HOST"] != "yuga-eu-1.internal" {
		t.Fatalf("expected DATABASE_HOST to use mesh host, got %q", env["DATABASE_HOST"])
	}
	if env["DATABASE_URL"] != "postgres://frameworks@yuga-eu-1.internal:5433/postgres?sslmode=disable" {
		t.Fatalf("expected DATABASE_URL to use mesh host, got %q", env["DATABASE_URL"])
	}
	if env["KAFKA_BROKERS"] != "central-eu-1.internal:9092,regional-eu-1.internal:9093" {
		t.Fatalf("expected KAFKA_BROKERS to use mesh hosts, got %q", env["KAFKA_BROKERS"])
	}
	if env["CLICKHOUSE_ADDR"] != "yuga-eu-1.internal:9000" {
		t.Fatalf("expected CLICKHOUSE_ADDR to use mesh host, got %q", env["CLICKHOUSE_ADDR"])
	}
	if env["CLICKHOUSE_HOST"] != "yuga-eu-1.internal" {
		t.Fatalf("expected CLICKHOUSE_HOST to use mesh host, got %q", env["CLICKHOUSE_HOST"])
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

func TestRegisterPublicServiceInstanceWithClientUsesResolvedGatewayMetadata(t *testing.T) {
	envFile := writeTestEnvFile(t, "LIVEPEER_ETH_ACCT_ADDR=0xabc123\n")
	manifest := &inventory.Manifest{
		RootDomain: "frameworks.network",
		EnvFile:    envFile,
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
		Host:      "core-1",
		ClusterID: "media-a",
		Phase:     orchestrator.PhaseApplications,
	}
	runtimeData := map[string]interface{}{
		"service_token": "svc-token",
	}
	registrar := &fakePublicServiceRegistrar{}

	var out bytes.Buffer
	if err := registerPublicServiceInstanceWithClient(context.Background(), &out, manifest, task, manifest.Hosts["core-1"], runtimeData, "", registrar); err != nil {
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

func TestValidateGatewayMeshCoverageRejectsGatewayOutsidePrivateerHosts(t *testing.T) {
	manifest := &inventory.Manifest{
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

func TestResolveManifestToRepoPath(t *testing.T) {
	tests := []struct {
		name        string
		manifestDir string
		relPath     string
		want        string
		wantErr     bool
	}{
		{
			name:        "env_file with parent traversal",
			manifestDir: "clusters/production",
			relPath:     "../../secrets/production.env",
			want:        "secrets/production.env",
		},
		{
			name:        "hosts_file in same directory",
			manifestDir: "clusters/production",
			relPath:     "hosts.enc.yaml",
			want:        "clusters/production/hosts.enc.yaml",
		},
		{
			name:        "absolute path rejected in repo mode",
			manifestDir: "clusters/production",
			relPath:     "/etc/frameworks/env",
			wantErr:     true,
		},
		{
			name:        "path escaping repo root is rejected",
			manifestDir: "clusters/production",
			relPath:     "../../../escape",
			wantErr:     true,
		},
		{
			name:        "root-level manifest same-dir ref",
			manifestDir: ".",
			relPath:     "secrets.env",
			want:        "secrets.env",
		},
		{
			name:        "root-level manifest parent escape rejected",
			manifestDir: ".",
			relPath:     "../outside",
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveManifestToRepoPath(tt.manifestDir, tt.relPath)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for relPath=%q, got %q", tt.relPath, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("resolveManifestToRepoPath(%q, %q) = %q, want %q", tt.manifestDir, tt.relPath, got, tt.want)
			}
		})
	}
}
