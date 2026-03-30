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
