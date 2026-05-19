package cmd

import (
	"bytes"
	"context"
	"errors"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"frameworks/cli/pkg/clusterderive"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
	"frameworks/cli/pkg/remoteaccess"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ingress"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"

	"github.com/spf13/cobra"
)

// testSharedSecrets provides the required shared platform secrets for test env files.
const testSharedSecrets = "SERVICE_TOKEN=test-token\nJWT_SECRET=test-jwt\nPASSWORD_RESET_SECRET=test-reset\nFIELD_ENCRYPTION_KEY=test-enc\nUSAGE_HASH_SECRET=test-hash\n"

type fakeFoghornClusterAssigner struct {
	calls     []*pb.AssignServiceToClusterRequest
	drains    []*pb.DrainServiceInstanceRequest
	services  []*pb.Service
	instances map[string][]*pb.ServiceInstance
	errFor    map[string]error
}

func (f *fakeFoghornClusterAssigner) AssignServiceToCluster(_ context.Context, req *pb.AssignServiceToClusterRequest) error {
	f.calls = append(f.calls, req)
	if f.errFor != nil {
		if err := f.errFor[req.GetClusterId()]; err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeFoghornClusterAssigner) DrainServiceInstance(_ context.Context, req *pb.DrainServiceInstanceRequest) (*pb.DrainServiceInstanceResponse, error) {
	f.drains = append(f.drains, req)
	return &pb.DrainServiceInstanceResponse{}, nil
}

func (f *fakeFoghornClusterAssigner) ListServices(_ context.Context, _ *pb.CursorPaginationRequest) (*pb.ListServicesResponse, error) {
	return &pb.ListServicesResponse{Services: f.services}, nil
}

func (f *fakeFoghornClusterAssigner) ListServiceInstances(_ context.Context, _, serviceID, _ string, _ *pb.CursorPaginationRequest) (*pb.ListServiceInstancesResponse, error) {
	return &pb.ListServiceInstancesResponse{Instances: f.instances[serviceID]}, nil
}

func fakePoolServices(names ...string) []*pb.Service {
	services := make([]*pb.Service, 0, len(names))
	for _, name := range names {
		services = append(services, &pb.Service{ServiceId: name, Type: name})
	}
	return services
}

func fakeServiceInstance(id, serviceID, nodeID, status string) *pb.ServiceInstance {
	return &pb.ServiceInstance{
		Id:         id,
		InstanceId: id,
		ServiceId:  serviceID,
		NodeId:     &nodeID,
		Status:     status,
	}
}

func TestQuartermasterBootstrapUsesFreshContextForAliasResolution(t *testing.T) {
	raw, err := os.ReadFile("cluster_provision.go")
	if err != nil {
		t.Fatalf("read cluster_provision.go: %v", err)
	}
	src := string(raw)
	if strings.Contains(src, "resolveSystemTenantIDViaQM(bootstrapCtx") {
		t.Fatalf("system tenant alias resolution must not reuse the Ansible bootstrap timeout context")
	}
	if strings.Contains(src, "resolveClusterOwnerTenantIDs(bootstrapCtx") {
		t.Fatalf("cluster owner alias resolution must not reuse the Ansible bootstrap timeout context")
	}

	bootstrapCall := strings.Index(src, "runServiceBootstrap(bootstrapCtx")
	resolveCtx := strings.Index(src, "resolveCtx, resolveCancel := context.WithTimeout(ctx, provisionInitializeTimeout)")
	resolveCall := strings.Index(src, "resolveSystemTenantIDViaQM(resolveCtx")
	if bootstrapCall < 0 || resolveCtx < 0 || resolveCall < 0 {
		t.Fatalf("expected bootstrap call, fresh resolve context, and resolve call in cluster_provision.go")
	}
	if bootstrapCall >= resolveCtx || resolveCtx >= resolveCall {
		t.Fatalf("fresh resolve context must be created after Quartermaster bootstrap and before alias resolution")
	}
}

func TestClusterProvisionDoesNotUseFixedWholeRunDeadline(t *testing.T) {
	raw, err := os.ReadFile("cluster_provision.go")
	if err != nil {
		t.Fatalf("read cluster_provision.go: %v", err)
	}
	if strings.Contains(string(raw), "context.WithTimeout(context.Background(), 30*time.Minute)") {
		t.Fatalf("cluster provision must not impose a fixed whole-run deadline")
	}
}

func TestRunProvisionPhaseReportsOwnTimeout(t *testing.T) {
	err := runProvisionPhase(context.Background(), time.Millisecond, "provision", func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "provision timed out after") {
		t.Fatalf("expected phase timeout error, got %v", err)
	}
}

func TestRunProvisionPhaseReportsParentInterruption(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	<-parent.Done()

	err := runProvisionPhase(parent, time.Hour, "provision", func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	if err == nil {
		t.Fatal("expected parent interruption error")
	}
	if !strings.Contains(err.Error(), "provision interrupted by parent context") {
		t.Fatalf("expected parent interruption error, got %v", err)
	}
	if strings.Contains(err.Error(), "timed out after 1h0m0s") {
		t.Fatalf("parent interruption must not be reported as the phase timeout: %v", err)
	}
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

func TestBootstrapInternalCertSANsIncludeHostInternalName(t *testing.T) {
	dnsNames, _ := bootstrapInternalCertSANs("decklog", "media-eu-1", "frameworks.network", inventory.Host{Name: "regional-eu-1"})
	for _, want := range []string{"decklog", "decklog.internal", "regional-eu-1.internal", "decklog.media-eu-1.frameworks.network"} {
		if !slices.Contains(dnsNames, want) {
			t.Fatalf("expected SAN %q in %v", want, dnsNames)
		}
	}
}

func TestReconcileServiceClusterAssignmentsWithClientAssignsMediaClusters(t *testing.T) {
	manifest := &inventory.Manifest{
		Profile: "dev",
		Hosts: map[string]inventory.Host{
			"core-1": {ExternalIP: "203.0.113.10"},
		},
		Clusters: map[string]inventory.ClusterConfig{
			"media-central-primary": {Type: "edge", Roles: []string{"media"}, Default: true},
			"core-central-primary":  {Type: "central"},
		},
		Services: map[string]inventory.ServiceConfig{
			"foghorn":          {Enabled: true, Host: "core-1"},
			"chandler":         {Enabled: true, Host: "core-1"},
			"livepeer-gateway": {Enabled: true, Host: "core-1"},
		},
	}
	assigner := &fakeFoghornClusterAssigner{
		services: fakePoolServices("foghorn", "chandler", "livepeer-gateway"),
		instances: map[string][]*pb.ServiceInstance{
			"foghorn":          {fakeServiceInstance("foghorn-core-1", "foghorn", "core-1", "running")},
			"chandler":         {fakeServiceInstance("chandler-core-1", "chandler", "core-1", "running")},
			"livepeer-gateway": {fakeServiceInstance("gateway-core-1", "livepeer-gateway", "core-1", "running")},
		},
	}

	var out bytes.Buffer
	if err := reconcileServiceClusterAssignmentsWithClient(context.Background(), &out, manifest, assigner); err != nil {
		t.Fatalf("reconcile returned error: %v", err)
	}

	// One assignment per pool service (foghorn/chandler/livepeer-gateway), each
	// targeting the default media cluster.
	if len(assigner.calls) != 3 {
		t.Fatalf("expected 3 assignment calls, got %d (%v)", len(assigner.calls), assigner.calls)
	}
	for _, call := range assigner.calls {
		if call.GetClusterId() != "media-central-primary" {
			t.Fatalf("expected media-central-primary assignment, got %q", call.GetClusterId())
		}
		if call.GetCount() != 0 {
			t.Fatalf("manifest reconciliation must assign explicit instances, got count=%d", call.GetCount())
		}
		if len(call.GetInstanceIds()) != 1 {
			t.Fatalf("expected one explicit instance id, got %v", call.GetInstanceIds())
		}
	}
	if len(assigner.drains) != 3 {
		t.Fatalf("expected existing assignments to be cleared first, got %d drains", len(assigner.drains))
	}

	output := out.String()
	if !strings.Contains(output, "Reconciling service-cluster assignments") {
		t.Fatalf("expected reconciliation banner in output, got %q", output)
	}
}

func TestReconcileServiceClusterAssignmentsWithClientAssignsDeclaredPoolInstances(t *testing.T) {
	manifest := &inventory.Manifest{
		Profile: "dev",
		Hosts: map[string]inventory.Host{
			"gateway-1": {ExternalIP: "203.0.113.10"},
			"gateway-2": {ExternalIP: "203.0.113.11"},
		},
		Clusters: map[string]inventory.ClusterConfig{
			"media-free-eu": {Type: "edge", Roles: []string{"media"}},
			"media-paid-eu": {Type: "edge", Roles: []string{"media"}},
			"core-eu":       {Type: "central"},
		},
		Services: map[string]inventory.ServiceConfig{
			"livepeer-gateway": {
				Enabled:  true,
				Hosts:    []string{"gateway-1", "gateway-2"},
				Clusters: []string{"media-free-eu", "media-paid-eu"},
			},
		},
	}
	assigner := &fakeFoghornClusterAssigner{
		services: fakePoolServices("foghorn", "chandler", "livepeer-gateway"),
		instances: map[string][]*pb.ServiceInstance{
			"livepeer-gateway": {
				fakeServiceInstance("gateway-inst-1", "livepeer-gateway", "gateway-1", "running"),
				fakeServiceInstance("gateway-inst-2", "livepeer-gateway", "gateway-2", "running"),
				fakeServiceInstance("old-gateway", "livepeer-gateway", "old-host", "running"),
			},
		},
	}

	if err := reconcileServiceClusterAssignmentsWithClient(context.Background(), &bytes.Buffer{}, manifest, assigner); err != nil {
		t.Fatalf("reconcile returned error: %v", err)
	}

	if len(assigner.drains) != 2 {
		t.Fatalf("expected manifest-owned gateway assignments to be cleared, got %d drains", len(assigner.drains))
	}
	for _, drain := range assigner.drains {
		if drain.GetInstanceId() == "old-gateway" {
			t.Fatalf("must not drain service assignments outside the active manifest hosts: %+v", assigner.drains)
		}
	}
	if len(assigner.calls) != 2 {
		t.Fatalf("expected one assignment call per logical cluster, got %d", len(assigner.calls))
	}
	for _, call := range assigner.calls {
		if call.GetCount() != 0 {
			t.Fatalf("manifest reconciliation must not use Count; got %d", call.GetCount())
		}
		gotIDs := strings.Join(call.GetInstanceIds(), ",")
		if gotIDs != "gateway-inst-1,gateway-inst-2" {
			t.Fatalf("assigned ids = %q, want declared gateway hosts only", gotIDs)
		}
	}
}

func TestReconcileServiceClusterAssignmentsWithClientAssignsAliasedPoolInstances(t *testing.T) {
	manifest := &inventory.Manifest{
		Profile: "dev",
		Hosts: map[string]inventory.Host{
			"regional-eu-1": {ExternalIP: "203.0.113.10"},
			"regional-us-1": {ExternalIP: "203.0.113.11"},
		},
		Clusters: map[string]inventory.ClusterConfig{
			"media-eu-1": {Type: "edge", Roles: []string{"media"}},
			"media-us-1": {Type: "edge", Roles: []string{"media"}},
		},
		Services: map[string]inventory.ServiceConfig{
			"foghorn-eu": {
				Enabled: true,
				Deploy:  "foghorn",
				Host:    "regional-eu-1",
				Cluster: "media-eu-1",
			},
			"foghorn-us": {
				Enabled: true,
				Deploy:  "foghorn",
				Host:    "regional-us-1",
				Cluster: "media-us-1",
			},
		},
	}
	assigner := &fakeFoghornClusterAssigner{
		services: fakePoolServices("foghorn"),
		instances: map[string][]*pb.ServiceInstance{
			"foghorn": {
				fakeServiceInstance("foghorn-eu", "foghorn", "regional-eu-1", "running"),
				fakeServiceInstance("foghorn-us", "foghorn", "regional-us-1", "running"),
			},
		},
	}

	if err := reconcileServiceClusterAssignmentsWithClient(context.Background(), &bytes.Buffer{}, manifest, assigner); err != nil {
		t.Fatalf("reconcile returned error: %v", err)
	}

	if len(assigner.calls) != 2 {
		t.Fatalf("expected one assignment per alias cluster, got %d", len(assigner.calls))
	}
	got := map[string]string{}
	for _, call := range assigner.calls {
		got[call.GetClusterId()] = strings.Join(call.GetInstanceIds(), ",")
	}
	if got["media-eu-1"] != "foghorn-eu" || got["media-us-1"] != "foghorn-us" {
		t.Fatalf("assignments = %+v, want eu/us split", got)
	}
	if len(assigner.drains) != 2 {
		t.Fatalf("expected existing assignments to be cleared once per manifest instance, got %+v", assigner.drains)
	}
}

func TestReconcileServiceClusterAssignmentsWithClientDrainsRemovedService(t *testing.T) {
	manifest := &inventory.Manifest{
		Profile: "dev",
		Hosts: map[string]inventory.Host{
			"core-1": {ExternalIP: "203.0.113.10"},
		},
		Services: map[string]inventory.ServiceConfig{
			"foghorn": {Enabled: false},
		},
	}
	assigner := &fakeFoghornClusterAssigner{
		services: fakePoolServices("foghorn"),
		instances: map[string][]*pb.ServiceInstance{
			"foghorn": {fakeServiceInstance("stale-foghorn", "foghorn", "core-1", "running")},
		},
	}

	if err := reconcileServiceClusterAssignmentsWithClient(context.Background(), &bytes.Buffer{}, manifest, assigner); err != nil {
		t.Fatalf("reconcile returned error: %v", err)
	}

	if len(assigner.drains) != 1 || assigner.drains[0].GetInstanceId() != "stale-foghorn" {
		t.Fatalf("expected stale service assignment drain, got %+v", assigner.drains)
	}
	if len(assigner.calls) != 0 {
		t.Fatalf("disabled service must not be assigned, got %+v", assigner.calls)
	}
}

func TestReconcileRemovedServicePlacementsKeepsAliasedPoolHosts(t *testing.T) {
	manifest := &inventory.Manifest{
		Profile: "dev",
		Hosts: map[string]inventory.Host{
			"central-eu-1":  {ExternalIP: "203.0.113.10"},
			"regional-eu-1": {ExternalIP: "203.0.113.11"},
			"regional-us-1": {ExternalIP: "203.0.113.12"},
		},
		Services: map[string]inventory.ServiceConfig{
			"chandler-eu": {
				Enabled: true,
				Deploy:  "chandler",
				Host:    "regional-eu-1",
				Cluster: "media-eu-1",
			},
			"chandler-us": {
				Enabled: true,
				Deploy:  "chandler",
				Host:    "regional-us-1",
				Cluster: "media-us-1",
			},
		},
	}
	assigner := &fakeFoghornClusterAssigner{
		services: fakePoolServices("chandler"),
		instances: map[string][]*pb.ServiceInstance{
			"chandler": {
				fakeServiceInstance("chandler-central", "chandler", "central-eu-1", "running"),
				fakeServiceInstance("chandler-eu", "chandler", "regional-eu-1", "running"),
				fakeServiceInstance("chandler-us", "chandler", "regional-us-1", "running"),
			},
		},
	}

	var cleaned []string
	cleanup := func(_ context.Context, placement removedServicePlacement) error {
		cleaned = append(cleaned, placement.serviceName+"@"+placement.nodeID)
		return nil
	}

	if err := reconcileRemovedServicePlacementsWithClient(context.Background(), &bytes.Buffer{}, manifest, orchestrator.PhaseApplications, assigner, cleanup); err != nil {
		t.Fatalf("reconcile returned error: %v", err)
	}

	if got := strings.Join(cleaned, ","); got != "chandler@central-eu-1" {
		t.Fatalf("cleanup targets = %q, want stale central chandler only", got)
	}
	if len(assigner.drains) != 1 || assigner.drains[0].GetInstanceId() != "chandler-central" {
		t.Fatalf("expected stale chandler assignment drain, got %+v", assigner.drains)
	}
}

func TestReconcileServiceClusterAssignmentsWithClientDoesNotDrainBeforeValidation(t *testing.T) {
	manifest := &inventory.Manifest{
		Profile: "dev",
		Hosts: map[string]inventory.Host{
			"core-1": {ExternalIP: "203.0.113.10"},
			"core-2": {ExternalIP: "203.0.113.11"},
		},
		Clusters: map[string]inventory.ClusterConfig{
			"media-central-primary": {Type: "edge", Roles: []string{"media"}, Default: true},
		},
		Services: map[string]inventory.ServiceConfig{
			"foghorn":  {Enabled: true, Host: "core-1"},
			"chandler": {Enabled: true, Host: "core-2"},
		},
	}
	assigner := &fakeFoghornClusterAssigner{
		services: fakePoolServices("foghorn", "chandler"),
		instances: map[string][]*pb.ServiceInstance{
			"foghorn":  {fakeServiceInstance("foghorn-core-1", "foghorn", "core-1", "running")},
			"chandler": {fakeServiceInstance("chandler-core-1", "chandler", "core-1", "running")},
		},
	}

	err := reconcileServiceClusterAssignmentsWithClient(context.Background(), &bytes.Buffer{}, manifest, assigner)
	if err == nil {
		t.Fatal("expected missing chandler host validation error")
	}
	if len(assigner.drains) != 0 {
		t.Fatalf("validation failure must not mutate existing assignments, got drains %+v", assigner.drains)
	}
	if len(assigner.calls) != 0 {
		t.Fatalf("validation failure must not assign instances, got calls %+v", assigner.calls)
	}
}

func TestReconcileRemovedServicePlacementsCleansStaleGatewayHost(t *testing.T) {
	manifest := &inventory.Manifest{
		Profile: "dev",
		Hosts: map[string]inventory.Host{
			"central-eu-1":  {ExternalIP: "203.0.113.10"},
			"regional-eu-1": {ExternalIP: "203.0.113.11"},
			"regional-us-1": {ExternalIP: "203.0.113.12"},
		},
		Services: map[string]inventory.ServiceConfig{
			"livepeer-gateway": {
				Enabled: true,
				Hosts:   []string{"regional-eu-1", "regional-us-1"},
			},
		},
	}
	assigner := &fakeFoghornClusterAssigner{
		services: fakePoolServices("livepeer-gateway"),
		instances: map[string][]*pb.ServiceInstance{
			"livepeer-gateway": {
				fakeServiceInstance("gateway-central", "livepeer-gateway", "central-eu-1", "running"),
				fakeServiceInstance("gateway-eu", "livepeer-gateway", "regional-eu-1", "running"),
				fakeServiceInstance("gateway-us", "livepeer-gateway", "regional-us-1", "running"),
			},
		},
	}

	var cleaned []string
	cleanup := func(_ context.Context, placement removedServicePlacement) error {
		cleaned = append(cleaned, placement.serviceName+"@"+placement.nodeID)
		return nil
	}

	if err := reconcileRemovedServicePlacementsWithClient(context.Background(), &bytes.Buffer{}, manifest, orchestrator.PhaseApplications, assigner, cleanup); err != nil {
		t.Fatalf("reconcile returned error: %v", err)
	}

	if got := strings.Join(cleaned, ","); got != "livepeer-gateway@central-eu-1" {
		t.Fatalf("cleanup targets = %q, want stale central gateway only", got)
	}
	if len(assigner.drains) != 1 || assigner.drains[0].GetInstanceId() != "gateway-central" {
		t.Fatalf("expected stale gateway assignment drain, got %+v", assigner.drains)
	}
}

func TestReconcileRemovedServicePlacementsDisabledServiceCleansAllInstances(t *testing.T) {
	manifest := &inventory.Manifest{
		Profile: "dev",
		Hosts: map[string]inventory.Host{
			"core-1": {ExternalIP: "203.0.113.10"},
		},
		Services: map[string]inventory.ServiceConfig{
			"foghorn": {Enabled: false},
		},
	}
	assigner := &fakeFoghornClusterAssigner{
		services: fakePoolServices("foghorn"),
		instances: map[string][]*pb.ServiceInstance{
			"foghorn": {fakeServiceInstance("foghorn-core-1", "foghorn", "core-1", "running")},
		},
	}

	var cleaned []string
	cleanup := func(_ context.Context, placement removedServicePlacement) error {
		cleaned = append(cleaned, placement.serviceName+"@"+placement.nodeID)
		return nil
	}

	if err := reconcileRemovedServicePlacementsWithClient(context.Background(), &bytes.Buffer{}, manifest, orchestrator.PhaseApplications, assigner, cleanup); err != nil {
		t.Fatalf("reconcile returned error: %v", err)
	}

	if got := strings.Join(cleaned, ","); got != "foghorn@core-1" {
		t.Fatalf("cleanup targets = %q, want disabled foghorn", got)
	}
	if len(assigner.drains) != 1 || assigner.drains[0].GetInstanceId() != "foghorn-core-1" {
		t.Fatalf("expected disabled service assignment drain, got %+v", assigner.drains)
	}
}

func TestReconcileRemovedServicePlacementsCleansDeletedServiceOnKnownHosts(t *testing.T) {
	manifest := &inventory.Manifest{
		Profile: "dev",
		Hosts: map[string]inventory.Host{
			"core-1": {ExternalIP: "203.0.113.10"},
		},
		Services: map[string]inventory.ServiceConfig{},
	}
	assigner := &fakeFoghornClusterAssigner{
		services: fakePoolServices("livepeer-gateway"),
		instances: map[string][]*pb.ServiceInstance{
			"livepeer-gateway": {
				fakeServiceInstance("gateway-core-1", "livepeer-gateway", "core-1", "running"),
				fakeServiceInstance("gateway-other-cluster", "livepeer-gateway", "other-core-1", "running"),
			},
		},
	}

	var cleaned []string
	cleanup := func(_ context.Context, placement removedServicePlacement) error {
		cleaned = append(cleaned, placement.serviceName+"@"+placement.nodeID+":"+strings.Join(placement.cleanupModes, "+"))
		return nil
	}

	if err := reconcileRemovedServicePlacementsWithClient(context.Background(), &bytes.Buffer{}, manifest, orchestrator.PhaseApplications, assigner, cleanup); err != nil {
		t.Fatalf("reconcile returned error: %v", err)
	}

	if got := strings.Join(cleaned, ","); got != "livepeer-gateway@core-1:native+docker" {
		t.Fatalf("cleanup targets = %q, want deleted gateway on known host only", got)
	}
	if len(assigner.drains) != 1 || assigner.drains[0].GetInstanceId() != "gateway-core-1" {
		t.Fatalf("expected deleted service assignment drain on known host, got %+v", assigner.drains)
	}
}

func TestReconcileRemovedServicePlacementsSkipsUnknownHosts(t *testing.T) {
	manifest := &inventory.Manifest{
		Profile: "dev",
		Hosts: map[string]inventory.Host{
			"regional-eu-1": {ExternalIP: "203.0.113.11"},
		},
		Services: map[string]inventory.ServiceConfig{
			"livepeer-gateway": {Enabled: true, Hosts: []string{"regional-eu-1"}},
		},
	}
	assigner := &fakeFoghornClusterAssigner{
		services: fakePoolServices("livepeer-gateway"),
		instances: map[string][]*pb.ServiceInstance{
			"livepeer-gateway": {
				fakeServiceInstance("gateway-missing", "livepeer-gateway", "central-eu-1", "running"),
			},
		},
	}

	if err := reconcileRemovedServicePlacementsWithClient(context.Background(), &bytes.Buffer{}, manifest, orchestrator.PhaseApplications, assigner, func(context.Context, removedServicePlacement) error {
		t.Fatal("cleanup must not run for an unknown host")
		return nil
	}); err != nil {
		t.Fatalf("reconcile returned error: %v", err)
	}
	if len(assigner.drains) != 0 {
		t.Fatalf("unknown hosts must not drain, got %+v", assigner.drains)
	}
}

func TestReconcileRemovedServicePlacementsSkipsNonDeployableRegistryServices(t *testing.T) {
	manifest := &inventory.Manifest{
		Profile: "dev",
		Hosts: map[string]inventory.Host{
			"central-eu-1":  {ExternalIP: "203.0.113.10"},
			"regional-eu-1": {ExternalIP: "203.0.113.11"},
		},
		Services: map[string]inventory.ServiceConfig{
			"livepeer-gateway": {Enabled: true, Hosts: []string{"regional-eu-1"}},
		},
		Observability: map[string]inventory.ServiceConfig{
			"telemetry": {Enabled: true, Host: "central-eu-1"},
		},
	}
	assigner := &fakeFoghornClusterAssigner{
		services: fakePoolServices("livepeer-gateway", "telemetry"),
		instances: map[string][]*pb.ServiceInstance{
			"livepeer-gateway": {
				fakeServiceInstance("gateway-central", "livepeer-gateway", "central-eu-1", "running"),
			},
			"telemetry": {
				fakeServiceInstance("telemetry-central", "telemetry", "central-eu-1", "running"),
			},
		},
	}

	var cleaned []string
	cleanup := func(_ context.Context, placement removedServicePlacement) error {
		cleaned = append(cleaned, placement.serviceName+"@"+placement.nodeID)
		return nil
	}

	if err := reconcileRemovedServicePlacementsWithClient(context.Background(), &bytes.Buffer{}, manifest, orchestrator.PhaseAll, assigner, cleanup); err != nil {
		t.Fatalf("reconcile returned error: %v", err)
	}

	if got := strings.Join(cleaned, ","); got != "livepeer-gateway@central-eu-1" {
		t.Fatalf("cleanup targets = %q, want stale deployable gateway only", got)
	}
	if len(assigner.drains) != 1 || assigner.drains[0].GetInstanceId() != "gateway-central" {
		t.Fatalf("expected stale gateway assignment drain only, got %+v", assigner.drains)
	}
}

func TestWriteRemovedServicePlacementDryRunPlanShowsCleanupActions(t *testing.T) {
	placements := []removedServicePlacement{
		{
			serviceName:  "livepeer-gateway",
			nodeID:       "central-eu-1",
			cleanupModes: []string{"native", "docker"},
		},
		{
			serviceName:  "metabase",
			nodeID:       "core-1",
			cleanupModes: []string{"docker"},
		},
	}

	var out bytes.Buffer
	writeRemovedServicePlacementDryRunPlan(&out, placements)

	got := out.String()
	for _, want := range []string{
		"Removed service cleanup plan:",
		"livepeer-gateway on central-eu-1: would drain pool assignment and cleanup (native+docker)",
		"metabase on core-1: would cleanup (docker)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("dry-run cleanup plan missing %q in:\n%s", want, got)
		}
	}
}

func TestReconcileServiceClusterAssignmentsWithClientReturnsClusterError(t *testing.T) {
	manifest := &inventory.Manifest{
		Profile: "dev",
		Hosts: map[string]inventory.Host{
			"core-1": {ExternalIP: "203.0.113.10"},
		},
		Clusters: map[string]inventory.ClusterConfig{
			"core-central-primary":  {Type: "central"},
			"media-central-primary": {Type: "edge", Roles: []string{"media"}, Default: true},
		},
		Services: map[string]inventory.ServiceConfig{
			"foghorn": {Enabled: true, Host: "core-1"},
		},
	}
	assigner := &fakeFoghornClusterAssigner{
		services: fakePoolServices("foghorn"),
		instances: map[string][]*pb.ServiceInstance{
			"foghorn": {fakeServiceInstance("foghorn-core-1", "foghorn", "core-1", "running")},
		},
		errFor: map[string]error{
			"media-central-primary": errors.New("no running foghorn"),
		},
	}

	err := reconcileServiceClusterAssignmentsWithClient(context.Background(), &bytes.Buffer{}, manifest, assigner)
	if err == nil {
		t.Fatal("expected reconciliation error")
	}
	if !strings.Contains(err.Error(), "media-central-primary") {
		t.Fatalf("expected cluster id in error, got %v", err)
	}
}

func TestMaybeReconcileBatchServiceClusterAssignmentsSkipsUnrelatedBatch(t *testing.T) {
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

	if err := maybeReconcileBatchServiceClusterAssignments(context.Background(), cmd, batch, manifest, map[string]any{}, nil); err != nil {
		t.Fatalf("expected no error for unrelated batch, got %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("expected no reconciliation output, got %q", out.String())
	}
}

func TestMaybeReconcileBatchServiceClusterAssignmentsRequiresQuartermasterRuntimeData(t *testing.T) {
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

	err := maybeReconcileBatchServiceClusterAssignments(context.Background(), cmd, batch, manifest, map[string]any{}, nil)
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

func TestProxyRouteServiceNamesIncludeDefaultCloudflareProxyServices(t *testing.T) {
	for _, service := range []string{
		"bridge",
		"chandler",
		"chartroom",
		"chatwoot",
		"foredeck",
		"grafana",
		"listmonk",
		"livepeer-gateway",
		"logbook",
		"metabase",
		"steward",
	} {
		if _, ok := proxyRouteServiceNames[service]; !ok {
			t.Fatalf("expected %s to be eligible for local reverse proxy routing", service)
		}
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

func TestValidateIngressBundleIDsRejectsUnsafeBundle(t *testing.T) {
	manifest := &inventory.Manifest{
		TLSBundles: map[string]inventory.TLSBundleConfig{
			"../../../etc/passwd": {Domains: []string{"x"}},
		},
	}
	err := validateIngressBundleIDs(manifest)
	if err == nil {
		t.Fatal("expected error on unsafe TLSBundle id")
	}
	if !strings.Contains(err.Error(), "tls_bundles") {
		t.Fatalf("error should name the offending key, got %v", err)
	}
}

func TestValidateIngressBundleIDsRejectsUnsafeIngressSiteRef(t *testing.T) {
	manifest := &inventory.Manifest{
		IngressSites: map[string]inventory.IngressSiteConfig{
			"bad": {TLSBundleID: "Has Space"},
		},
	}
	err := validateIngressBundleIDs(manifest)
	if err == nil {
		t.Fatal("expected error on unsafe IngressSite tls_bundle_id")
	}
	if !strings.Contains(err.Error(), "ingress_sites") {
		t.Fatalf("error should name the offending key, got %v", err)
	}
}

func TestValidateIngressBundleIDsAcceptsCanonical(t *testing.T) {
	manifest := &inventory.Manifest{
		TLSBundles: map[string]inventory.TLSBundleConfig{
			"wildcard-frameworks-network": {Domains: []string{"*.frameworks.network"}},
		},
		IngressSites: map[string]inventory.IngressSiteConfig{
			"bridge-graphql": {TLSBundleID: "wildcard-frameworks-network"},
			"http-only":      {}, // no bundle id is fine
		},
	}
	if err := validateIngressBundleIDs(manifest); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTLSBundleIDIsAlwaysSafe(t *testing.T) {
	// All root_domain inputs that the inventory layer accepts must produce
	// bundle ids ingress.IsValidBundleID accepts, otherwise an uppercase or
	// dotted-only domain would generate ids Privateer later rejects.
	cases := []struct {
		kind, root string
		want       string
	}{
		{"wildcard", "frameworks.network", "wildcard-frameworks-network"},
		{"wildcard", "Frameworks.Network", "wildcard-frameworks-network"},
		{"apex", "EXAMPLE.COM", "apex-example-com"},
		{"wildcard", "core-central-primary.frameworks.network", "wildcard-core-central-primary-frameworks-network"},
	}
	for _, tc := range cases {
		got := clusterderive.TLSBundleID(tc.kind, tc.root)
		if got != tc.want {
			t.Errorf("clusterderive.TLSBundleID(%q,%q) = %q, want %q", tc.kind, tc.root, got, tc.want)
		}
		if !ingress.IsValidBundleID(got) {
			t.Errorf("clusterderive.TLSBundleID(%q,%q) = %q is not a valid bundle id", tc.kind, tc.root, got)
		}
	}
}

func TestApplyProxySiteIngressTLSDefaultsSafeID(t *testing.T) {
	site := map[string]any{}
	applyProxySiteIngressTLSDefaults(site, "wildcard-frameworks-network")
	if site["tls_mode"] != "files" {
		t.Errorf("tls_mode = %v, want files", site["tls_mode"])
	}
	if site["tls_cert_path"] != "/etc/frameworks/ingress/tls/wildcard-frameworks-network/tls.crt" {
		t.Errorf("tls_cert_path = %v", site["tls_cert_path"])
	}
	if site["tls_key_path"] != "/etc/frameworks/ingress/tls/wildcard-frameworks-network/tls.key" {
		t.Errorf("tls_key_path = %v", site["tls_key_path"])
	}
}

func TestApplyProxySiteIngressTLSDefaultsRejectsUnsafeID(t *testing.T) {
	for _, bad := range []string{"", "../../../etc/passwd", "has/slash", "has space", "Wildcard"} {
		site := map[string]any{}
		applyProxySiteIngressTLSDefaults(site, bad)
		if len(site) != 0 {
			t.Errorf("unsafe id %q populated site %+v; expected no defaults", bad, site)
		}
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

	metadata, err := serviceRegistrationMetadata("livepeer-gateway", "central-eu-1", "media-central-primary", manifest, map[string]any{}, "", testLoadSharedEnv(t, manifest), nil)
	if err != nil {
		t.Fatalf("serviceRegistrationMetadata returned error: %v", err)
	}
	if metadata[servicedefs.LivepeerGatewayMetadataWalletAddress] != "0xabc123" {
		t.Fatalf("expected wallet metadata from resolved env, got %v", metadata)
	}
	if _, ok := metadata[servicedefs.LivepeerGatewayMetadataPublicHost]; ok {
		t.Fatalf("public_host must be synthesized by DiscoverServices, got static metadata %v", metadata)
	}
	if metadata[servicedefs.LivepeerGatewayMetadataPublicPort] != "443" {
		t.Fatalf("expected public port 443, got %q", metadata[servicedefs.LivepeerGatewayMetadataPublicPort])
	}
	if metadata[servicedefs.LivepeerGatewayMetadataPublicScheme] != "https" {
		t.Fatalf("expected public scheme https, got %q", metadata[servicedefs.LivepeerGatewayMetadataPublicScheme])
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

func TestBuildServiceEnvVarsClusterEnvOverridesSharedAndIsOverriddenByInline(t *testing.T) {
	// Shared env declares a platform-wide S3 default. Cluster env (US cell)
	// overrides those secrets with the region-specific values. Per-service
	// inline Config still wins over both — this proves the four-step merge
	// order: shared → cluster → per-service env_file → inline config.
	sharedFile := writeTestEnvFile(t, "STORAGE_S3_ACCESS_KEY=platform-default\nSTORAGE_S3_BUCKET=platform-bucket\nSTORAGE_S3_ENDPOINT=https://platform.example\n")
	usEnv := writeTestEnvFile(t, "STORAGE_S3_ACCESS_KEY=r2-us-key\nSTORAGE_S3_BUCKET=frameworks-us-east\nSTORAGE_S3_ENDPOINT=https://r2.example\n")

	manifest := &inventory.Manifest{
		Profile:    "dev",
		RootDomain: "frameworks.network",
		EnvFiles:   []string{sharedFile},
		Clusters: map[string]inventory.ClusterConfig{
			"media-us-1": {
				Name:     "Media US East 1",
				EnvFiles: []string{usEnv},
			},
			"media-eu-1": {Name: "Media EU 1"},
		},
		Services: map[string]inventory.ServiceConfig{
			"foghorn": {
				Enabled: true,
				Config: map[string]string{
					// Inline config wins over both shared and cluster.
					"STORAGE_S3_BUCKET": "inline-override",
				},
			},
		},
	}

	sharedEnv := testLoadSharedEnv(t, manifest)
	clusterEnvs, err := inventory.LoadClusterEnvs(manifest, "", "")
	if err != nil {
		t.Fatalf("LoadClusterEnvs: %v", err)
	}

	usEnvVars, err := buildServiceEnvVars(&orchestrator.Task{
		Name:      "foghorn",
		Type:      "foghorn",
		ServiceID: "foghorn",
		ClusterID: "media-us-1",
	}, manifest, map[string]any{}, "", "", sharedEnv, clusterEnvs)
	if err != nil {
		t.Fatalf("US buildServiceEnvVars: %v", err)
	}

	if got := usEnvVars["STORAGE_S3_ACCESS_KEY"]; got != "r2-us-key" {
		t.Errorf("cluster env did not override shared: STORAGE_S3_ACCESS_KEY = %q, want r2-us-key", got)
	}
	if got := usEnvVars["STORAGE_S3_ENDPOINT"]; got != "https://r2.example" {
		t.Errorf("cluster endpoint = %q, want https://r2.example", got)
	}
	if got := usEnvVars["STORAGE_S3_BUCKET"]; got != "inline-override" {
		t.Errorf("inline service config did not override cluster env: STORAGE_S3_BUCKET = %q, want inline-override", got)
	}

	euEnvVars, err := buildServiceEnvVars(&orchestrator.Task{
		Name:      "foghorn",
		Type:      "foghorn",
		ServiceID: "foghorn",
		ClusterID: "media-eu-1",
	}, manifest, map[string]any{}, "", "", sharedEnv, clusterEnvs)
	if err != nil {
		t.Fatalf("EU buildServiceEnvVars: %v", err)
	}

	// EU cluster has no env_files entry so it sees the platform default.
	if got := euEnvVars["STORAGE_S3_ACCESS_KEY"]; got != "platform-default" {
		t.Errorf("EU cluster without env_files leaked US value or lost shared: STORAGE_S3_ACCESS_KEY = %q, want platform-default", got)
	}
	if got := euEnvVars["STORAGE_S3_ENDPOINT"]; got != "https://platform.example" {
		t.Errorf("EU endpoint = %q, want https://platform.example", got)
	}
}

func TestBuildServiceEnvVarsLoadsSplitManifestEnvFiles(t *testing.T) {
	baseEnv := writeTestEnvFile(t, strings.Join([]string{
		"ARBITRUM_RPC_ENDPOINT=https://arb.example",
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
	}, manifest, map[string]any{}, "", "", testLoadSharedEnv(t, manifest), nil)
	if err != nil {
		t.Fatalf("buildServiceEnvVars returned error: %v", err)
	}

	if got := env["eth_url"]; got != "https://arb.example" {
		t.Fatalf("expected eth_url from first env file, got %q", got)
	}
	if got := env["eth_acct_addr"]; got != "0xabc123" {
		t.Fatalf("expected eth_acct_addr from second env file, got %q", got)
	}
	if got := env["gateway_host"]; got != "" {
		t.Fatalf("gateway_host must not be auto-derived for an M:N gateway pool, got %q", got)
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
	}, manifest, map[string]any{}, "", "", testLoadSharedEnv(t, manifest), nil)
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
	}, manifest, map[string]any{}, "", "", testLoadSharedEnv(t, manifest), nil)
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
		"PLATFORM_ADMIN_PASSWORD=bootstrap-only",
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

	env, err := buildServiceEnvVars(task, manifest, map[string]any{}, "", "", testLoadSharedEnv(t, manifest), nil)
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
	if _, ok := env["PLATFORM_ADMIN_PASSWORD"]; ok {
		t.Fatal("expected bootstrap-only password to be absent from service env")
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

	_, err := buildServiceEnvVars(task, manifest, map[string]any{}, "", "", testLoadSharedEnv(t, manifest), nil)
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

	if _, err := buildServiceEnvVars(task, manifest, map[string]any{}, "", "", testLoadSharedEnv(t, manifest), nil); err != nil {
		t.Fatalf("expected base64 CA envs to satisfy prod validation, got %v", err)
	}
}

func TestBuildServiceEnvVarsUsesMeshHostsForBackendDependencies(t *testing.T) {
	manifest := &inventory.Manifest{
		Profile:    "dev",
		RootDomain: "frameworks.network",
		Hosts: map[string]inventory.Host{
			"central-eu-1":  {ExternalIP: "10.0.0.10", WireguardIP: "10.88.0.10", Roles: []string{"control"}},
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
				Instances: []inventory.PostgresInstance{
					{
						Name: "chatwoot",
						Host: "central-eu-1",
						Port: 5432,
					},
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

	env, err := buildServiceEnvVars(task, manifest, map[string]any{}, "", "", testLoadSharedEnv(t, manifest), nil)
	if err != nil {
		t.Fatalf("buildServiceEnvVars returned error: %v", err)
	}

	if env["DATABASE_HOST"] != "yuga-eu-1.internal" {
		t.Fatalf("expected DATABASE_HOST to use mesh host, got %q", env["DATABASE_HOST"])
	}
	if env["DATABASE_URL"] != "postgres://foghorn@yuga-eu-1.internal:5433/foghorn?sslmode=disable" {
		t.Fatalf("expected DATABASE_URL to use mesh host with service-level user and database, got %q", env["DATABASE_URL"])
	}
	if env["POSTGRES_CHATWOOT_HOST"] != "127.0.0.1" {
		t.Fatalf("expected POSTGRES_CHATWOOT_HOST to use loopback for colocated Postgres, got %q", env["POSTGRES_CHATWOOT_HOST"])
	}
	if env["POSTGRES_CHATWOOT_PORT"] != "5432" {
		t.Fatalf("expected POSTGRES_CHATWOOT_PORT to use named instance port, got %q", env["POSTGRES_CHATWOOT_PORT"])
	}
	if env["POSTGRES_CHATWOOT_ADDR"] != "127.0.0.1:5432" {
		t.Fatalf("expected POSTGRES_CHATWOOT_ADDR to use loopback endpoint, got %q", env["POSTGRES_CHATWOOT_ADDR"])
	}
	if env["KAFKA_BROKERS"] != "central-eu-1.internal:9092,regional-eu-1.internal:9093" {
		t.Fatalf("expected KAFKA_BROKERS to use mesh hosts, got %q", env["KAFKA_BROKERS"])
	}
	if env["CLICKHOUSE_ADDR"] != "yuga-eu-1.internal:9000" {
		t.Fatalf("expected CLICKHOUSE_ADDR to use mesh host, got %q", env["CLICKHOUSE_ADDR"])
	}
	if env["REDIS_FOGHORN_ADDR"] != "127.0.0.1:6379" {
		t.Fatalf("expected REDIS_FOGHORN_ADDR to use loopback for colocated Redis, got %q", env["REDIS_FOGHORN_ADDR"])
	}
	if env["REDIS_CHATWOOT_ADDR"] != "127.0.0.1:6380" {
		t.Fatalf("expected REDIS_CHATWOOT_ADDR to use loopback for colocated Redis, got %q", env["REDIS_CHATWOOT_ADDR"])
	}
	if _, ok := env["CHANDLER_HOST"]; ok {
		t.Fatalf("expected CHANDLER_HOST not to be auto-generated as an internal dependency, got %q", env["CHANDLER_HOST"])
	}
	if env["CHANDLER_INTERNAL_URL"] != "http://central-eu-1.internal:18020" {
		t.Fatalf("expected CHANDLER_INTERNAL_URL to use Chandler host mesh URL, got %q", env["CHANDLER_INTERNAL_URL"])
	}
	if env["LISTMONK_URL"] != "http://central-eu-1.internal:9001" {
		t.Fatalf("expected LISTMONK_URL to use mesh host, got %q", env["LISTMONK_URL"])
	}
	if env["CHATWOOT_HOST"] != "central-eu-1.internal" {
		t.Fatalf("expected CHATWOOT_HOST to use mesh host, got %q", env["CHATWOOT_HOST"])
	}
}

func TestBuildServiceEnvVarsUsesMeshIPForColocatedChatwootRedis(t *testing.T) {
	manifest := &inventory.Manifest{
		Profile:    "dev",
		RootDomain: "frameworks.network",
		Hosts: map[string]inventory.Host{
			"central-eu-1": {ExternalIP: "10.0.0.10", WireguardIP: "10.88.0.10", Roles: []string{"control"}},
		},
		Infrastructure: inventory.InfrastructureConfig{
			Redis: &inventory.RedisConfig{
				Enabled: true,
				Instances: []inventory.RedisInstance{
					{Name: "chatwoot", Host: "central-eu-1", Port: 6380, Password: "${REDIS_CHATWOOT_PASSWORD}"},
				},
			},
		},
		Services: map[string]inventory.ServiceConfig{
			"chatwoot": {Enabled: true, Host: "central-eu-1", Port: 18092},
		},
	}
	task := &orchestrator.Task{
		Name:      "chatwoot",
		Type:      "chatwoot",
		ServiceID: "chatwoot",
		Host:      "central-eu-1",
		Phase:     orchestrator.PhaseApplications,
	}

	env, err := buildServiceEnvVars(task, manifest, map[string]any{}, "", "", map[string]string{
		"REDIS_CHATWOOT_PASSWORD": "redis-secret",
	}, nil)
	if err != nil {
		t.Fatalf("buildServiceEnvVars returned error: %v", err)
	}
	if env["REDIS_CHATWOOT_ADDR"] != "10.88.0.10:6380" {
		t.Fatalf("expected REDIS_CHATWOOT_ADDR to use mesh IP for colocated Chatwoot Redis, got %q", env["REDIS_CHATWOOT_ADDR"])
	}
	if env["REDIS_CHATWOOT_PASSWORD"] != "redis-secret" {
		t.Fatalf("expected REDIS_CHATWOOT_PASSWORD to come from Redis instance config, got %q", env["REDIS_CHATWOOT_PASSWORD"])
	}
}

func TestBuildServiceEnvVarsIncludesFoghornRedisPasswordInURL(t *testing.T) {
	manifest := &inventory.Manifest{
		Profile:    "dev",
		RootDomain: "frameworks.network",
		Hosts: map[string]inventory.Host{
			"media-eu-1": {ExternalIP: "10.0.0.20", WireguardIP: "10.88.0.20", Roles: []string{"media"}},
		},
		Infrastructure: inventory.InfrastructureConfig{
			Redis: &inventory.RedisConfig{
				Enabled: true,
				Instances: []inventory.RedisInstance{
					{Name: "foghorn", Host: "media-eu-1", Port: 6380, Password: "${REDIS_FOGHORN_PASSWORD}"},
				},
			},
		},
		Services: map[string]inventory.ServiceConfig{
			"foghorn": {Enabled: true, Host: "media-eu-1"},
		},
	}
	task := &orchestrator.Task{
		Name:      "foghorn",
		Type:      "foghorn",
		ServiceID: "foghorn",
		Host:      "media-eu-1",
		Phase:     orchestrator.PhaseApplications,
	}

	env, err := buildServiceEnvVars(task, manifest, map[string]any{}, "", "", map[string]string{
		"REDIS_FOGHORN_PASSWORD": "redis secret",
	}, nil)
	if err != nil {
		t.Fatalf("buildServiceEnvVars returned error: %v", err)
	}
	if env["REDIS_URL"] != "redis://:redis+secret@127.0.0.1:6380" {
		t.Fatalf("expected Foghorn REDIS_URL to include password, got %q", env["REDIS_URL"])
	}
}

func TestBuildServiceEnvVarsIncludesFoghornSentinelPassword(t *testing.T) {
	manifest := &inventory.Manifest{
		Profile:    "dev",
		RootDomain: "frameworks.network",
		Hosts: map[string]inventory.Host{
			"regional-eu-1": {ExternalIP: "10.0.0.11", WireguardIP: "10.88.0.11"},
			"regional-eu-2": {ExternalIP: "10.0.0.12", WireguardIP: "10.88.0.12"},
			"regional-eu-3": {ExternalIP: "10.0.0.13", WireguardIP: "10.88.0.13"},
		},
		Infrastructure: inventory.InfrastructureConfig{
			Redis: &inventory.RedisConfig{
				Enabled: true,
				Instances: []inventory.RedisInstance{{
					Name:       "foghorn",
					Mode:       "sentinel",
					Host:       "regional-eu-1",
					Port:       6379,
					Password:   "${REDIS_FOGHORN_PASSWORD}",
					MasterName: "foghorn",
					Sentinels: []inventory.RedisSentinelNode{
						{Host: "regional-eu-1"},
						{Host: "regional-eu-2"},
						{Host: "regional-eu-3"},
					},
				}},
			},
		},
		Services: map[string]inventory.ServiceConfig{
			"foghorn": {Enabled: true, Host: "regional-eu-2"},
		},
	}
	task := &orchestrator.Task{
		Name:      "foghorn-eu",
		Type:      "foghorn",
		ServiceID: "foghorn",
		Host:      "regional-eu-2",
		Phase:     orchestrator.PhaseApplications,
	}

	env, err := buildServiceEnvVars(task, manifest, map[string]any{}, "", "", map[string]string{
		"REDIS_FOGHORN_PASSWORD": "redis secret",
	}, nil)
	if err != nil {
		t.Fatalf("buildServiceEnvVars returned error: %v", err)
	}
	if env["REDIS_MODE"] != "sentinel" {
		t.Fatalf("REDIS_MODE = %q, want sentinel", env["REDIS_MODE"])
	}
	if env["REDIS_PASSWORD"] != "redis secret" {
		t.Fatalf("REDIS_PASSWORD = %q, want redis secret", env["REDIS_PASSWORD"])
	}
	if env["REDIS_SENTINEL_PASSWORD"] != "redis secret" {
		t.Fatalf("REDIS_SENTINEL_PASSWORD = %q, want redis secret", env["REDIS_SENTINEL_PASSWORD"])
	}
}

func TestBuildServiceEnvVarsBindsDeclaredPostgresInstanceDatabase(t *testing.T) {
	manifest := &inventory.Manifest{
		Profile:    "dev",
		RootDomain: "frameworks.network",
		Hosts: map[string]inventory.Host{
			"regional-eu-1": {ExternalIP: "10.0.0.11"},
			"regional-eu-2": {ExternalIP: "10.0.0.12"},
		},
		Clusters: map[string]inventory.ClusterConfig{
			"media-eu-1": {Name: "Media EU"},
		},
		Infrastructure: inventory.InfrastructureConfig{
			Postgres: &inventory.PostgresConfig{
				Enabled: true,
				Instances: []inventory.PostgresInstance{
					{
						Name:     "foghorn-eu",
						Host:     "regional-eu-1",
						Port:     5432,
						Password: "instance-secret",
						Databases: []inventory.DatabaseConfig{
							{Name: "foghorn_eu", Owner: "foghorn_eu"},
						},
					},
				},
			},
		},
		Services: map[string]inventory.ServiceConfig{
			"foghorn-eu": {
				Enabled: true,
				Deploy:  "foghorn",
				Host:    "regional-eu-2",
				Cluster: "media-eu-1",
			},
		},
	}

	env, err := buildServiceEnvVars(&orchestrator.Task{
		Name:      "foghorn-eu@regional-eu-2",
		Type:      "foghorn",
		ServiceID: "foghorn-eu",
		Host:      "regional-eu-2",
		ClusterID: "media-eu-1",
		Phase:     orchestrator.PhaseApplications,
	}, manifest, map[string]any{}, "", "", map[string]string{}, map[string]map[string]string{
		"media-eu-1": {"DATABASE_PASSWORD": "wrong-cluster-default"},
	})
	if err != nil {
		t.Fatalf("buildServiceEnvVars returned error: %v", err)
	}

	if got := env["DATABASE_HOST"]; got != "regional-eu-1.internal" {
		t.Fatalf("DATABASE_HOST = %q, want regional-eu-1.internal", got)
	}
	if got := env["DATABASE_USER"]; got != "foghorn_eu" {
		t.Fatalf("DATABASE_USER = %q, want foghorn_eu", got)
	}
	if got := env["DATABASE_NAME"]; got != "foghorn_eu" {
		t.Fatalf("DATABASE_NAME = %q, want foghorn_eu", got)
	}
	if got := env["DATABASE_PASSWORD"]; got != "instance-secret" {
		t.Fatalf("DATABASE_PASSWORD = %q, want instance-secret", got)
	}
	if !strings.Contains(env["DATABASE_URL"], "foghorn_eu:instance-secret@regional-eu-1.internal:5432/foghorn_eu") {
		t.Fatalf("DATABASE_URL did not use declared instance credentials: %q", env["DATABASE_URL"])
	}
}

func TestYugabyteDatabaseMetadataUsesClusterPasswordForServiceDatabase(t *testing.T) {
	manifest := &inventory.Manifest{
		Services: map[string]inventory.ServiceConfig{
			"foghorn-eu": {
				Enabled: true,
				Deploy:  "foghorn",
				Cluster: "media-eu-1",
			},
			"foghorn-us": {
				Enabled: true,
				Deploy:  "foghorn",
				Cluster: "media-us-1",
			},
		},
	}
	databases := []inventory.DatabaseConfig{
		{Name: "foghorn_eu", Owner: "foghorn_eu"},
		{Name: "foghorn_us", Owner: "foghorn_us"},
		{Name: "quartermaster", Owner: "quartermaster"},
	}
	items := yugabyteDatabaseConfigsToMetadata(databases, manifest, map[string]string{
		"DATABASE_PASSWORD": "shared-secret",
	}, map[string]map[string]string{
		"media-eu-1": {"DATABASE_PASSWORD": "eu-secret"},
		"media-us-1": {"DATABASE_PASSWORD": "us-secret"},
	}, "shared-secret")

	got := map[string]string{}
	for _, item := range items {
		got[item["name"]] = item["password"]
	}
	if got["foghorn_eu"] != "eu-secret" {
		t.Fatalf("foghorn_eu password = %q, want eu-secret", got["foghorn_eu"])
	}
	if got["foghorn_us"] != "us-secret" {
		t.Fatalf("foghorn_us password = %q, want us-secret", got["foghorn_us"])
	}
	if got["quartermaster"] != "shared-secret" {
		t.Fatalf("quartermaster password = %q, want shared-secret", got["quartermaster"])
	}
}

func TestYugabyteDatabaseMetadataExpandsClusterScopedServiceDatabase(t *testing.T) {
	manifest := &inventory.Manifest{
		Services: map[string]inventory.ServiceConfig{
			"foghorn-eu": {
				Enabled: true,
				Deploy:  "foghorn",
				Cluster: "media-eu-1",
			},
			"foghorn-us": {
				Enabled: true,
				Deploy:  "foghorn",
				Cluster: "media-us-1",
			},
		},
	}
	databases := expandedYugabyteDatabaseConfigs([]inventory.DatabaseConfig{
		{Name: "foghorn"},
		{Name: "quartermaster"},
	}, manifest)
	items := yugabyteDatabaseConfigsToMetadata(databases, manifest, map[string]string{
		"DATABASE_PASSWORD": "shared-secret",
	}, map[string]map[string]string{
		"media-eu-1": {"DATABASE_PASSWORD": "eu-secret"},
		"media-us-1": {"DATABASE_PASSWORD": "us-secret"},
	}, "shared-secret")

	got := map[string]string{}
	for _, item := range items {
		got[item["name"]] = item["password"]
	}
	if _, ok := got["foghorn"]; ok {
		t.Fatalf("logical foghorn database should expand to cluster databases, got base entry in %#v", items)
	}
	if got["foghorn_eu"] != "eu-secret" {
		t.Fatalf("foghorn_eu password = %q, want eu-secret", got["foghorn_eu"])
	}
	if got["foghorn_us"] != "us-secret" {
		t.Fatalf("foghorn_us password = %q, want us-secret", got["foghorn_us"])
	}
	if got["quartermaster"] != "shared-secret" {
		t.Fatalf("quartermaster password = %q, want shared-secret", got["quartermaster"])
	}
}

func TestBuildTaskConfigPostgresInstanceDoesNotInheritYugabyteSettings(t *testing.T) {
	manifest := &inventory.Manifest{
		Channel: "stable",
		Infrastructure: inventory.InfrastructureConfig{
			Postgres: &inventory.PostgresConfig{
				Enabled: true,
				Engine:  "yugabyte",
				Version: "2.25.1.0",
				Port:    5433,
				Nodes:   []inventory.PostgresNode{{Host: "yuga-eu-1", ID: 1}},
				Instances: []inventory.PostgresInstance{
					{
						Name:     "chatwoot",
						Host:     "central-eu-1",
						Port:     5432,
						Version:  "16",
						Password: "chatwoot-secret",
						Databases: []inventory.DatabaseConfig{
							{Name: "chatwoot", Owner: "chatwoot"},
						},
					},
				},
			},
		},
	}
	task := &orchestrator.Task{
		Name:       "postgres@chatwoot",
		Type:       "postgres",
		ServiceID:  "postgres",
		InstanceID: "chatwoot",
		Host:       "central-eu-1",
		Phase:      orchestrator.PhaseInfrastructure,
	}

	config, err := buildTaskConfig(task, manifest, map[string]any{}, false, "", map[string]string{}, nil, nil)
	if err != nil {
		t.Fatalf("buildTaskConfig returned error: %v", err)
	}

	if config.Mode != "native" {
		t.Fatalf("postgres instance mode = %q, want native", config.Mode)
	}
	if config.Version != "16" {
		t.Fatalf("postgres instance version = %q, want 16", config.Version)
	}
	if config.Port != 5432 {
		t.Fatalf("postgres instance port = %d, want 5432", config.Port)
	}
	if got := config.Metadata["postgres_password"]; got != "chatwoot-secret" {
		t.Fatalf("postgres_password = %#v, want chatwoot-secret", got)
	}
	databases, ok := config.Metadata["databases"].([]map[string]string)
	if !ok || len(databases) != 1 {
		t.Fatalf("databases metadata = %#v, want one database", config.Metadata["databases"])
	}
	if databases[0]["name"] != "chatwoot" || databases[0]["owner"] != "chatwoot" {
		t.Fatalf("databases[0] = %#v, want chatwoot/chatwoot", databases[0])
	}
}

func TestBuildServiceEnvVarsDerivesAllChandlerInternalURLs(t *testing.T) {
	manifest := &inventory.Manifest{
		Profile:    "dev",
		RootDomain: "frameworks.network",
		Hosts: map[string]inventory.Host{
			"central-eu-1":  {ExternalIP: "10.0.0.10"},
			"regional-eu-1": {ExternalIP: "10.0.0.11"},
		},
		Services: map[string]inventory.ServiceConfig{
			"foghorn": {
				Enabled: true,
				Host:    "central-eu-1",
			},
			"chandler": {
				Enabled: true,
				Hosts:   []string{"central-eu-1", "regional-eu-1"},
				Port:    18020,
			},
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

	env, err := buildServiceEnvVars(task, manifest, map[string]any{}, "", "", testLoadSharedEnv(t, manifest), nil)
	if err != nil {
		t.Fatalf("buildServiceEnvVars returned error: %v", err)
	}

	want := "http://central-eu-1.internal:18020,http://regional-eu-1.internal:18020"
	if env["CHANDLER_INTERNAL_URL"] != want {
		t.Fatalf("expected all Chandler internal URLs, got %q", env["CHANDLER_INTERNAL_URL"])
	}
}

func TestBuildServiceEnvVarsDerivesOrderedGatewayMCPURLs(t *testing.T) {
	envFile := writeTestEnvFile(t, testSharedSecrets+strings.Join([]string{
		"DATABASE_PASSWORD=test-db-pass",
		"GATEWAY_PUBLIC_URL=https://api.frameworks.network",
	}, "\n")+"\n")
	manifest := &inventory.Manifest{
		Profile:  "production",
		EnvFiles: []string{envFile},
		Clusters: map[string]inventory.ClusterConfig{
			"core-eu":     {Region: "eu-west"},
			"regional-eu": {Region: "eu-west"},
			"regional-us": {Region: "us-west"},
		},
		Hosts: map[string]inventory.Host{
			"central-1": {ExternalIP: "10.0.0.10", Cluster: "core-eu"},
			"z-eu":      {ExternalIP: "10.0.0.11", Cluster: "regional-eu"},
			"a-us":      {ExternalIP: "10.0.0.12", Cluster: "regional-us"},
			"yuga-1":    {ExternalIP: "10.0.0.13", Cluster: "core-eu"},
			"kafka-1":   {ExternalIP: "10.0.0.14", Cluster: "core-eu"},
		},
		Infrastructure: inventory.InfrastructureConfig{
			Postgres: &inventory.PostgresConfig{Enabled: true, Engine: "yugabyte", Port: 5433, Nodes: []inventory.PostgresNode{{Host: "yuga-1", ID: 1}}},
			Kafka:    &inventory.KafkaConfig{Enabled: true, Brokers: []inventory.KafkaBroker{{Host: "kafka-1", ID: 1, Port: 9092}}},
		},
		Services: map[string]inventory.ServiceConfig{
			"bridge-eu": {Enabled: true, Deploy: "bridge", Host: "z-eu"},
			"bridge-us": {Enabled: true, Deploy: "bridge", Host: "a-us"},
			"skipper":   {Enabled: true, Host: "central-1"},
		},
	}

	env, err := buildServiceEnvVars(&orchestrator.Task{
		Name:      "skipper",
		Type:      "skipper",
		ServiceID: "skipper",
		Host:      "central-1",
		Phase:     orchestrator.PhaseApplications,
	}, manifest, map[string]any{}, "", "", testLoadSharedEnv(t, manifest), nil)
	if err != nil {
		t.Fatalf("buildServiceEnvVars skipper: %v", err)
	}

	want := "http://z-eu.internal:18000/mcp,http://a-us.internal:18000/mcp"
	if env["GATEWAY_MCP_URLS"] != want {
		t.Fatalf("GATEWAY_MCP_URLS = %q, want %q", env["GATEWAY_MCP_URLS"], want)
	}
	if env["GATEWAY_MCP_URL"] != "http://z-eu.internal:18000/mcp" {
		t.Fatalf("GATEWAY_MCP_URL = %q", env["GATEWAY_MCP_URL"])
	}
}

func TestBuildServiceEnvVarsCoversRuntimeEnvDependencies(t *testing.T) {
	envFile := writeTestEnvFile(t, testSharedSecrets+strings.Join([]string{
		"DATABASE_PASSWORD=test-db-pass",
		"CLICKHOUSE_PASSWORD=test-ch-pass",
		"CHATWOOT_API_TOKEN=test-chatwoot-token",
		"CLOUDFLARE_API_TOKEN=test-cf-token",
		"CLOUDFLARE_ZONE_ID=test-zone",
		"CLOUDFLARE_ACCOUNT_ID=test-account",
		"ACME_EMAIL=ops@example.com",
		"GATEWAY_PUBLIC_URL=https://api.frameworks.network",
		"NAVIGATOR_INTERNAL_CA_ROOT_CERT_PEM_B64=cm9vdA==",
		"NAVIGATOR_INTERNAL_CA_INTERMEDIATE_CERT_PEM_B64=aW50ZXJtZWRpYXRl",
		"NAVIGATOR_INTERNAL_CA_INTERMEDIATE_KEY_PEM_B64=a2V5",
	}, "\n")+"\n")

	manifest := &inventory.Manifest{
		Profile:    "production",
		RootDomain: "frameworks.network",
		EnvFiles:   []string{envFile},
		Hosts: map[string]inventory.Host{
			"central-eu-1": {ExternalIP: "10.0.0.10", Roles: []string{"control"}},
			"yuga-eu-1":    {ExternalIP: "10.0.0.11", Roles: []string{"infrastructure"}},
			"kafka-eu-1":   {ExternalIP: "10.0.0.12", Roles: []string{"infrastructure"}},
			"ch-eu-1":      {ExternalIP: "10.0.0.13", Roles: []string{"infrastructure"}},
		},
		Clusters: map[string]inventory.ClusterConfig{
			"core-central-primary": {Name: "Core Central Primary"},
		},
		Infrastructure: inventory.InfrastructureConfig{
			Postgres: &inventory.PostgresConfig{
				Enabled: true,
				Engine:  "yugabyte",
				Port:    5433,
				Nodes:   []inventory.PostgresNode{{Host: "yuga-eu-1", ID: 1}},
			},
			ClickHouse: &inventory.ClickHouseConfig{
				Enabled: true,
				Host:    "ch-eu-1",
				Port:    9000,
			},
			Kafka: &inventory.KafkaConfig{
				Enabled:   true,
				ClusterID: "core-central-primary",
				Brokers:   []inventory.KafkaBroker{{Host: "kafka-eu-1", ID: 1, Port: 9092}},
			},
		},
		Services: map[string]inventory.ServiceConfig{
			"bridge":           {Enabled: true, Host: "central-eu-1"},
			"commodore":        {Enabled: true, Host: "central-eu-1"},
			"quartermaster":    {Enabled: true, Host: "central-eu-1"},
			"purser":           {Enabled: true, Host: "central-eu-1"},
			"periscope-query":  {Enabled: true, Host: "central-eu-1"},
			"periscope-ingest": {Enabled: true, Host: "central-eu-1"},
			"decklog":          {Enabled: true, Host: "central-eu-1"},
			"signalman":        {Enabled: true, Host: "central-eu-1"},
			"navigator":        {Enabled: true, Host: "central-eu-1"},
			"chandler":         {Enabled: true, Host: "central-eu-1"},
			"foghorn":          {Enabled: true, Host: "central-eu-1"},
			"deckhand":         {Enabled: true, Host: "central-eu-1"},
			"skipper":          {Enabled: true, Host: "central-eu-1"},
			"chatwoot":         {Enabled: true, Host: "central-eu-1", Port: 18092},
		},
	}

	sharedEnv := testLoadSharedEnv(t, manifest)
	runtimeData := map[string]any{"service_token": "runtime-service-token"}
	cases := []struct {
		serviceID string
		want      map[string]string
		keys      []string
	}{
		{
			serviceID: "bridge",
			want: map[string]string{
				"COMMODORE_GRPC_ADDR":     "commodore.internal:19001",
				"PERISCOPE_GRPC_ADDR":     "periscope-query.internal:19004",
				"PURSER_GRPC_ADDR":        "purser.internal:19003",
				"QUARTERMASTER_GRPC_ADDR": "quartermaster.internal:19002",
				"SIGNALMAN_GRPC_ADDR":     "signalman.internal:19005",
				"DECKLOG_GRPC_ADDR":       "decklog.internal:18006",
				"SKIPPER_SPOKE_URL":       "http://skipper.internal:18018/mcp/spoke",
			},
			keys: []string{"SERVICE_TOKEN", "JWT_SECRET", "USAGE_HASH_SECRET", "GRPC_TLS_CA_PATH"},
		},
		{
			serviceID: "quartermaster",
			want: map[string]string{
				"NAVIGATOR_GRPC_ADDR": "navigator.internal:18011",
				"DECKLOG_GRPC_ADDR":   "decklog.internal:18006",
				"PURSER_GRPC_ADDR":    "purser.internal:19003",
			},
			keys: []string{"DATABASE_URL", "SERVICE_TOKEN", "JWT_SECRET", "GRPC_TLS_CA_PATH", "GRPC_TLS_CERT_PATH", "GRPC_TLS_KEY_PATH"},
		},
		{
			serviceID: "commodore",
			want: map[string]string{
				"QUARTERMASTER_GRPC_ADDR": "quartermaster.internal:19002",
				"PURSER_GRPC_ADDR":        "purser.internal:19003",
				"DECKLOG_GRPC_ADDR":       "decklog.internal:18006",
			},
			keys: []string{"DATABASE_URL", "SERVICE_TOKEN", "JWT_SECRET", "PASSWORD_RESET_SECRET", "GRPC_TLS_CERT_PATH", "GRPC_TLS_KEY_PATH"},
		},
		{
			serviceID: "purser",
			want:      map[string]string{"QUARTERMASTER_GRPC_ADDR": "quartermaster.internal:19002"},
			keys:      []string{"DATABASE_URL", "SERVICE_TOKEN", "JWT_SECRET", "GRPC_TLS_CERT_PATH", "GRPC_TLS_KEY_PATH"},
		},
		{
			serviceID: "navigator",
			want:      map[string]string{"QUARTERMASTER_GRPC_ADDR": "quartermaster.internal:19002", "NAVIGATOR_GRPC_PORT": "18011", "NAVIGATOR_PORT": "18010"},
			keys:      []string{"DATABASE_URL", "SERVICE_TOKEN", "FIELD_ENCRYPTION_KEY", "BRAND_DOMAIN", "ACME_EMAIL", "CLOUDFLARE_API_TOKEN", "CLOUDFLARE_ZONE_ID", "CLOUDFLARE_ACCOUNT_ID"},
		},
		{
			serviceID: "periscope-query",
			want:      map[string]string{"QUARTERMASTER_GRPC_ADDR": "quartermaster.internal:19002"},
			keys:      []string{"DATABASE_URL", "CLICKHOUSE_ADDR", "CLICKHOUSE_DB", "CLICKHOUSE_USER", "CLICKHOUSE_PASSWORD", "JWT_SECRET", "SERVICE_TOKEN", "GRPC_TLS_CERT_PATH", "GRPC_TLS_KEY_PATH"},
		},
		{
			serviceID: "periscope-ingest",
			want:      map[string]string{"QUARTERMASTER_GRPC_ADDR": "quartermaster.internal:19002"},
			keys:      []string{"CLICKHOUSE_ADDR", "CLICKHOUSE_DB", "CLICKHOUSE_USER", "CLICKHOUSE_PASSWORD", "KAFKA_BROKERS", "KAFKA_CLUSTER_ID", "SERVICE_TOKEN"},
		},
		{
			serviceID: "decklog",
			want:      map[string]string{"QUARTERMASTER_GRPC_ADDR": "quartermaster.internal:19002", "DECKLOG_GRPC_ADDR": "decklog.internal:18006"},
			keys:      []string{"KAFKA_BROKERS", "KAFKA_CLUSTER_ID", "SERVICE_TOKEN", "GRPC_TLS_CERT_PATH", "GRPC_TLS_KEY_PATH"},
		},
		{
			serviceID: "signalman",
			want:      map[string]string{"QUARTERMASTER_GRPC_ADDR": "quartermaster.internal:19002", "DECKLOG_GRPC_ADDR": "decklog.internal:18006"},
			keys:      []string{"KAFKA_BROKERS", "KAFKA_CLUSTER_ID", "SERVICE_TOKEN", "JWT_SECRET"},
		},
		{
			serviceID: "foghorn",
			want: map[string]string{
				"FOGHORN_CONTROL_BIND_ADDR": ":18019",
				"CHANDLER_INTERNAL_URL":     "http://central-eu-1.internal:18020",
				"COMMODORE_GRPC_ADDR":       "commodore.internal:19001",
				"QUARTERMASTER_GRPC_ADDR":   "quartermaster.internal:19002",
				"NAVIGATOR_GRPC_ADDR":       "navigator.internal:18011",
			},
			keys: []string{"DATABASE_URL", "SERVICE_TOKEN", "DECKLOG_GRPC_ADDR", "PURSER_GRPC_ADDR", "GRPC_TLS_CERT_PATH", "GRPC_TLS_KEY_PATH"},
		},
		{
			serviceID: "deckhand",
			want:      map[string]string{"CHATWOOT_HOST": "central-eu-1.internal", "CHATWOOT_PORT": "18092", "DECKLOG_GRPC_ADDR": "decklog.internal:18006"},
			keys:      []string{"SERVICE_TOKEN", "CHATWOOT_API_TOKEN", "QUARTERMASTER_GRPC_ADDR", "PURSER_GRPC_ADDR", "GRPC_TLS_CERT_PATH", "GRPC_TLS_KEY_PATH"},
		},
		{
			serviceID: "skipper",
			want: map[string]string{
				"GATEWAY_MCP_URL":  "http://central-eu-1.internal:18000/mcp",
				"GATEWAY_MCP_URLS": "http://central-eu-1.internal:18000/mcp",
			},
			keys: []string{"DATABASE_URL", "KAFKA_BROKERS", "KAFKA_CLUSTER_ID", "GATEWAY_PUBLIC_URL", "GATEWAY_MCP_URL", "GATEWAY_MCP_URLS", "GRPC_TLS_CERT_PATH", "GRPC_TLS_KEY_PATH"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.serviceID, func(t *testing.T) {
			env, err := buildServiceEnvVars(&orchestrator.Task{
				Name:      tc.serviceID,
				Type:      tc.serviceID,
				ServiceID: tc.serviceID,
				Host:      "central-eu-1",
				ClusterID: "core-central-primary",
				Phase:     orchestrator.PhaseApplications,
			}, manifest, runtimeData, "", "", sharedEnv, nil)
			if err != nil {
				t.Fatalf("buildServiceEnvVars returned error: %v", err)
			}
			for key, want := range tc.want {
				if want == "" {
					continue
				}
				if got := env[key]; got != want {
					t.Fatalf("%s = %q, want %q", key, got, want)
				}
			}
			for _, key := range tc.keys {
				if strings.TrimSpace(env[key]) == "" {
					t.Fatalf("expected %s to be populated", key)
				}
			}
			if missing := missingRequiredGeneratedEnv(manifest, tc.serviceID, env); len(missing) > 0 {
				t.Fatalf("generated runtime env missing: %v", missing)
			}
			for _, req := range topology.RequiredServiceEnv(tc.serviceID) {
				if req.TargetServiceID != "" && !manifestServiceEnabledForDeploy(manifest, req.TargetServiceID) {
					continue
				}
				value := strings.TrimSpace(env[req.EnvKey])
				if value == "" {
					t.Fatalf("expected generated runtime env %s to be populated", req.EnvKey)
				}
				if strings.Contains(req.EnvKey, "GRPC_ADDR") && !strings.Contains(value, ".internal:") {
					t.Fatalf("%s = %q, want rendered mesh address instead of binary fallback", req.EnvKey, value)
				}
				if strings.Contains(strings.ToUpper(req.EnvKey), "URL") && !strings.Contains(value, ".internal:") {
					t.Fatalf("%s = %q, want rendered mesh address instead of binary fallback", req.EnvKey, value)
				}
			}
		})
	}
}

func TestBuildServiceEnvVarsEscapesDatabaseURLPassword(t *testing.T) {
	envFile := writeTestEnvFile(t, testSharedSecrets+"DATABASE_PASSWORD=pa:ss@/word?#%\n")
	manifest := &inventory.Manifest{
		Profile:  "dev",
		EnvFiles: []string{envFile},
		Hosts: map[string]inventory.Host{
			"yuga-eu-1": {WireguardIP: "10.88.112.204"},
		},
		Infrastructure: inventory.InfrastructureConfig{
			Postgres: &inventory.PostgresConfig{
				Enabled: true,
				Engine:  "yugabyte",
				Port:    5433,
				Nodes:   []inventory.PostgresNode{{Host: "yuga-eu-1", ID: 1}},
			},
		},
	}
	task := &orchestrator.Task{
		Name:      "quartermaster",
		Type:      "quartermaster",
		ServiceID: "quartermaster",
		Host:      "central-eu-1",
		Phase:     orchestrator.PhaseApplications,
	}

	env, err := buildServiceEnvVars(task, manifest, map[string]any{}, "", "", testLoadSharedEnv(t, manifest), nil)
	if err != nil {
		t.Fatalf("buildServiceEnvVars returned error: %v", err)
	}
	parsed, err := url.Parse(env["DATABASE_URL"])
	if err != nil {
		t.Fatalf("DATABASE_URL should parse: %v", err)
	}
	password, _ := parsed.User.Password()
	if password != "pa:ss@/word?#%" {
		t.Fatalf("DATABASE_URL password was not preserved after URL parsing: %q", password)
	}
	if parsed.User.Username() != "quartermaster" {
		t.Fatalf("expected service database user, got %q", parsed.User.Username())
	}
	if parsed.Host != "yuga-eu-1.internal:5433" {
		t.Fatalf("expected mesh host in DATABASE_URL, got %q", parsed.Host)
	}
}

func TestBuildServiceEnvVarsUsesSharedPeriscopeDatabaseRole(t *testing.T) {
	envFile := writeTestEnvFile(t, testSharedSecrets+"DATABASE_PASSWORD=periscope-pass\n")
	manifest := &inventory.Manifest{
		Profile:  "dev",
		EnvFiles: []string{envFile},
		Hosts: map[string]inventory.Host{
			"central-eu-1": {WireguardIP: "10.88.0.10"},
		},
		Infrastructure: inventory.InfrastructureConfig{
			Postgres: &inventory.PostgresConfig{
				Enabled: true,
				Host:    "central-eu-1",
				Port:    5432,
			},
		},
	}
	task := &orchestrator.Task{
		Name:      "periscope-query",
		Type:      "periscope-query",
		ServiceID: "periscope-query",
		Host:      "central-eu-1",
		Phase:     orchestrator.PhaseApplications,
	}

	env, err := buildServiceEnvVars(task, manifest, map[string]any{}, "", "", testLoadSharedEnv(t, manifest), nil)
	if err != nil {
		t.Fatalf("buildServiceEnvVars returned error: %v", err)
	}
	parsed, err := url.Parse(env["DATABASE_URL"])
	if err != nil {
		t.Fatalf("DATABASE_URL should parse: %v", err)
	}
	if got := parsed.User.Username(); got != "periscope" {
		t.Fatalf("expected periscope database user, got %q", got)
	}
	if got := strings.TrimPrefix(parsed.Path, "/"); got != "periscope" {
		t.Fatalf("expected periscope database name, got %q", got)
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

	config, err := buildTaskConfig(task, manifest, map[string]any{}, false, "", map[string]string{}, nil, nil)
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

	controllerTask := &orchestrator.Task{
		Name:       "kafka-controller-101",
		Type:       "kafka-controller",
		ServiceID:  "kafka-controller",
		InstanceID: "101",
		Host:       "regional-eu-1",
		Phase:      orchestrator.PhaseInfrastructure,
	}
	controllerConfig, err := buildTaskConfig(controllerTask, manifest, map[string]any{}, false, "", map[string]string{}, nil, nil)
	if err != nil {
		t.Fatalf("buildTaskConfig for controller returned error: %v", err)
	}
	if got, _ := controllerConfig.Metadata["bind_host"].(string); got != "10.88.0.11" {
		t.Fatalf("expected controller bind_host to use mesh IP, got %q", got)
	}
}

func TestInfrastructureInitializeDeferralIncludesKafka(t *testing.T) {
	for _, taskType := range []string{"yugabyte", "kafka", "kafka-controller", "kafka-mirrormaker"} {
		if !deferInfrastructureInitialize(taskType) {
			t.Fatalf("expected %s initialization to be deferred", taskType)
		}
	}
	if deferInfrastructureInitialize("clickhouse") {
		t.Fatal("did not expect clickhouse initialization to be deferred")
	}
}

func TestKafkaBrokerRegistrationLagDetection(t *testing.T) {
	err := errors.New("org.apache.kafka.common.errors.InvalidReplicationFactorException: Unable to replicate the partition 3 time(s): The target replication factor of 3 cannot be reached because only 2 broker(s) are registered")
	if !isKafkaBrokerRegistrationLag(err) {
		t.Fatal("expected broker registration lag error to be retryable")
	}
	if isKafkaBrokerRegistrationLag(errors.New("some other kafka error")) {
		t.Fatal("unexpected retry classification for unrelated error")
	}
}

func TestBuildTaskConfigKafkaMirrorMakerRendersHubAndSpokeLinks(t *testing.T) {
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{
			"regional-eu-1": {WireguardIP: "10.88.0.11", Labels: map[string]string{"region": "eu-west"}},
			"regional-eu-2": {WireguardIP: "10.88.0.12", Labels: map[string]string{"region": "eu-west"}},
			"regional-eu-3": {WireguardIP: "10.88.0.13", Labels: map[string]string{"region": "eu-west"}},
			"regional-us-1": {WireguardIP: "10.88.1.11", Labels: map[string]string{"region": "us-east"}},
			"regional-us-2": {WireguardIP: "10.88.1.12", Labels: map[string]string{"region": "us-east"}},
			"regional-us-3": {WireguardIP: "10.88.1.13", Labels: map[string]string{"region": "us-east"}},
		},
		Infrastructure: inventory.InfrastructureConfig{
			Kafka: &inventory.KafkaConfig{
				Enabled: true,
				Version: "4.2.0",
				Brokers: []inventory.KafkaBroker{
					{Host: "regional-eu-1", ID: 1, Port: 9092},
					{Host: "regional-eu-2", ID: 2, Port: 9092},
					{Host: "regional-eu-3", ID: 3, Port: 9092},
				},
				Regional: []inventory.RegionalKafkaCluster{
					{
						RegionID: "us-east",
						Brokers: []inventory.KafkaBroker{
							{Host: "regional-us-1", ID: 11, Port: 9092},
							{Host: "regional-us-2", ID: 12, Port: 9092},
							{Host: "regional-us-3", ID: 13, Port: 9092},
						},
						MirrorTopics: []string{"analytics_events", "service_events"},
					},
				},
				MirrorMaker: &inventory.KafkaMirrorMakerConfig{
					Enabled:   true,
					Host:      "regional-eu-1",
					TaskCount: 2,
				},
			},
		},
	}
	task := &orchestrator.Task{
		Name:      "kafka-mirrormaker",
		Type:      "kafka-mirrormaker",
		ServiceID: "kafka-mirrormaker",
		Host:      "regional-eu-1",
		Phase:     orchestrator.PhaseInfrastructure,
	}

	config, err := buildTaskConfig(task, manifest, map[string]any{}, false, "", map[string]string{}, nil, nil)
	if err != nil {
		t.Fatalf("buildTaskConfig returned error: %v", err)
	}

	target := config.Metadata["target"].(map[string]any)
	if target["alias"] != "eu-west" {
		t.Fatalf("target alias = %v, want eu-west", target["alias"])
	}

	sources := config.Metadata["sources"].([]map[string]any)
	if len(sources) != 1 || sources[0]["alias"] != "us-east" {
		t.Fatalf("sources = %#v, want one us-east source", sources)
	}
	if sources[0]["bootstrap_servers"] != "10.88.1.11:9092,10.88.1.12:9092,10.88.1.13:9092" {
		t.Fatalf("source bootstrap_servers = %v", sources[0]["bootstrap_servers"])
	}
	if config.Metadata["local_cluster_alias"] != "eu-west" {
		t.Fatalf("local_cluster_alias = %v, want eu-west", config.Metadata["local_cluster_alias"])
	}

	if _, ok := config.Metadata["fanout_targets"]; ok {
		t.Fatalf("MM2 config must not mirror aggregate analytics back to regional Signalman")
	}
}

// TestBuildTaskConfigKafkaMirrorMakerThreeRegionTopology verifies one
// aggregator plus N regional sources: one fan-in source per regional Kafka and
// no aggregator-to-regional fanout.
func TestBuildTaskConfigKafkaMirrorMakerThreeRegionTopology(t *testing.T) {
	manifest := threeRegionKafkaManifest()
	manifest.Infrastructure.Kafka.MirrorMaker = &inventory.KafkaMirrorMakerConfig{
		Enabled:   true,
		Host:      "regional-eu-1",
		TaskCount: 2,
	}

	task := &orchestrator.Task{
		Name:      "kafka-mirrormaker",
		Type:      "kafka-mirrormaker",
		ServiceID: "kafka-mirrormaker",
		Host:      "regional-eu-1",
		Phase:     orchestrator.PhaseInfrastructure,
	}

	config, err := buildTaskConfig(task, manifest, map[string]any{}, false, "", map[string]string{}, nil, nil)
	if err != nil {
		t.Fatalf("buildTaskConfig: %v", err)
	}

	target := config.Metadata["target"].(map[string]any)
	if target["alias"] != "eu-west" {
		t.Fatalf("target = %v, want eu-west aggregator", target["alias"])
	}

	sources := config.Metadata["sources"].([]map[string]any)
	wantSourceAliases := map[string]bool{"us-east": false, "ap-south": false}
	for _, src := range sources {
		alias := src["alias"].(string)
		if _, ok := wantSourceAliases[alias]; !ok {
			t.Fatalf("unexpected source alias %q (regional↔regional link?)", alias)
		}
		wantSourceAliases[alias] = true
		if !strings.Contains(src["topics"].(string), "analytics_events") {
			t.Errorf("source %s topics missing analytics_events: %v", alias, src["topics"])
		}
		if !strings.Contains(src["topics"].(string), "decklog_events_dlq") {
			t.Errorf("source %s topics missing DLQ: %v", alias, src["topics"])
		}
	}
	for alias, seen := range wantSourceAliases {
		if !seen {
			t.Errorf("missing source for region %s", alias)
		}
	}

	if _, ok := config.Metadata["fanout_targets"]; ok {
		t.Fatalf("MM2 config must not include aggregator-to-regional fanout: %#v", config.Metadata["fanout_targets"])
	}
}

func TestBuildServiceEnvVarsSetsMirrorPrefixesForEveryPeriscopeReplica(t *testing.T) {
	manifest := &inventory.Manifest{
		RootDomain: "frameworks.network",
		Hosts: map[string]inventory.Host{
			"regional-eu-1": {WireguardIP: "10.88.0.11", Labels: map[string]string{"region": "eu-west"}},
			"regional-us-1": {WireguardIP: "10.88.1.11", Labels: map[string]string{"region": "us-east"}},
		},
		Clusters: map[string]inventory.ClusterConfig{
			"media-eu-1": {Region: "eu-west"},
			"media-us-1": {Region: "us-east"},
		},
		Infrastructure: inventory.InfrastructureConfig{
			Kafka: &inventory.KafkaConfig{
				Enabled:   true,
				ClusterID: "eu-kafka",
				Brokers:   []inventory.KafkaBroker{{Host: "regional-eu-1", ID: 1, Port: 9092}},
				Regional: []inventory.RegionalKafkaCluster{
					{
						RegionID:  "us-east",
						ClusterID: "us-kafka",
						Brokers:   []inventory.KafkaBroker{{Host: "regional-us-1", ID: 11, Port: 9092}},
					},
				},
			},
		},
	}

	euPeriscope := &orchestrator.Task{Type: "periscope-ingest", ServiceID: "periscope-ingest", Host: "regional-eu-1", ClusterID: "media-eu-1"}
	euEnv, err := buildServiceEnvVars(euPeriscope, manifest, map[string]any{}, "", "", testLoadSharedEnv(t, manifest), nil)
	if err != nil {
		t.Fatalf("buildServiceEnvVars eu periscope: %v", err)
	}
	if euEnv["MIRROR_REGION_PREFIXES"] != "us-east" {
		t.Fatalf("eu periscope MIRROR_REGION_PREFIXES = %q, want us-east", euEnv["MIRROR_REGION_PREFIXES"])
	}

	usSignalman := &orchestrator.Task{Type: "signalman", ServiceID: "signalman", Host: "regional-us-1", ClusterID: "media-us-1"}
	usEnv, err := buildServiceEnvVars(usSignalman, manifest, map[string]any{}, "", "", testLoadSharedEnv(t, manifest), nil)
	if err != nil {
		t.Fatalf("buildServiceEnvVars us signalman: %v", err)
	}
	if usEnv["MIRROR_REGION_PREFIXES"] != "" {
		t.Fatalf("us signalman MIRROR_REGION_PREFIXES = %q, want empty", usEnv["MIRROR_REGION_PREFIXES"])
	}

	usPeriscope := &orchestrator.Task{Type: "periscope-ingest", ServiceID: "periscope-ingest", Host: "regional-us-1", ClusterID: "media-us-1"}
	periscopeEnv, err := buildServiceEnvVars(usPeriscope, manifest, map[string]any{}, "", "", testLoadSharedEnv(t, manifest), nil)
	if err != nil {
		t.Fatalf("buildServiceEnvVars us periscope: %v", err)
	}
	if periscopeEnv["MIRROR_REGION_PREFIXES"] != "us-east" {
		t.Fatalf("regional periscope MIRROR_REGION_PREFIXES = %q, want us-east", periscopeEnv["MIRROR_REGION_PREFIXES"])
	}
}

func TestBuildServiceEnvVarsSelectsKafkaFromHostRegionForGenericRegionalService(t *testing.T) {
	manifest := &inventory.Manifest{
		RootDomain: "frameworks.network",
		Hosts: map[string]inventory.Host{
			"regional-eu-1": {WireguardIP: "10.88.0.11", Labels: map[string]string{"region": "eu-west"}},
			"regional-us-1": {WireguardIP: "10.88.1.11", Labels: map[string]string{"region": "us-east"}},
		},
		Infrastructure: inventory.InfrastructureConfig{
			Kafka: &inventory.KafkaConfig{
				Enabled:   true,
				ClusterID: "eu-kafka",
				Brokers:   []inventory.KafkaBroker{{Host: "regional-eu-1", ID: 1, Port: 9092}},
				Regional: []inventory.RegionalKafkaCluster{
					{
						RegionID:  "us-east",
						ClusterID: "us-kafka",
						Brokers:   []inventory.KafkaBroker{{Host: "regional-us-1", ID: 11, Port: 9092}},
					},
				},
			},
		},
	}

	usDecklog := &orchestrator.Task{Type: "decklog", ServiceID: "decklog", Host: "regional-us-1"}
	usEnv, err := buildServiceEnvVars(usDecklog, manifest, map[string]any{}, "", "", testLoadSharedEnv(t, manifest), nil)
	if err != nil {
		t.Fatalf("buildServiceEnvVars us decklog: %v", err)
	}
	if usEnv["KAFKA_BROKERS"] != "regional-us-1.internal:9092" {
		t.Fatalf("us KAFKA_BROKERS = %q, want regional US broker", usEnv["KAFKA_BROKERS"])
	}
	if usEnv["KAFKA_CLUSTER_ID"] != "us-kafka" {
		t.Fatalf("us KAFKA_CLUSTER_ID = %q, want us-kafka", usEnv["KAFKA_CLUSTER_ID"])
	}

	euDecklog := &orchestrator.Task{Type: "decklog", ServiceID: "decklog", Host: "regional-eu-1"}
	euEnv, err := buildServiceEnvVars(euDecklog, manifest, map[string]any{}, "", "", testLoadSharedEnv(t, manifest), nil)
	if err != nil {
		t.Fatalf("buildServiceEnvVars eu decklog: %v", err)
	}
	if euEnv["KAFKA_BROKERS"] != "regional-eu-1.internal:9092" {
		t.Fatalf("eu KAFKA_BROKERS = %q, want regional EU broker", euEnv["KAFKA_BROKERS"])
	}
	if euEnv["KAFKA_CLUSTER_ID"] != "eu-kafka" {
		t.Fatalf("eu KAFKA_CLUSTER_ID = %q, want eu-kafka", euEnv["KAFKA_CLUSTER_ID"])
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
	}, manifest, map[string]any{}, false, "", map[string]string{}, nil, nil)
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
	}, manifest, map[string]any{}, false, "", map[string]string{}, nil, nil)
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
	}, manifest, map[string]any{}, false, "", map[string]string{}, nil, nil)
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

func TestBuildTaskConfigManagedBundleIDHasCanonicalTLSPaths(t *testing.T) {
	// Privateer-managed bundles must use the canonical on-disk paths under
	// ingress.TLSRoot regardless of any tls_cert_path / tls_key_path /
	// tls_mode in TLSBundle or IngressSite metadata. Letting metadata win
	// would let nginx be aimed at a different file than the one Privateer
	// rotates.
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
				// These should be ignored — the site is bundle-managed.
				"tls_cert_path": "/operator/legacy/bridge.crt",
				"tls_key_path":  "/operator/legacy/bridge.key",
				"tls_mode":      "internal",
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
					// path_prefix is non-TLS metadata and remains overridable.
					"path_prefix": "/graphql",
					// Re-asserted at the IngressSite level too: still ignored.
					"tls_cert_path": "/site/level/bridge.crt",
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
	}, manifest, map[string]any{}, false, "", map[string]string{}, nil, nil)
	if err != nil {
		t.Fatalf("buildTaskConfig returned error: %v", err)
	}
	sites, ok := cfg.Metadata["proxy_sites"].([]map[string]any)
	if !ok || len(sites) != 1 {
		t.Fatalf("proxy_sites missing or wrong type: %#v", cfg.Metadata["proxy_sites"])
	}
	site := sites[0]
	if got := site["tls_cert_path"]; got != "/etc/frameworks/ingress/tls/bridge-cert/tls.crt" {
		t.Fatalf("tls_cert_path = %v; managed bundles must use canonical path", got)
	}
	if got := site["tls_key_path"]; got != "/etc/frameworks/ingress/tls/bridge-cert/tls.key" {
		t.Fatalf("tls_key_path = %v; managed bundles must use canonical path", got)
	}
	if got := site["tls_mode"]; got != "files" {
		t.Fatalf("tls_mode = %v; managed bundles must be files", got)
	}
	if got := site["path_prefix"]; got != "/graphql" {
		t.Fatalf("path_prefix = %v; non-TLS metadata is still overridable", got)
	}
}

func TestBuildTaskConfigUnmanagedSiteRetainsManualTLSPaths(t *testing.T) {
	// A site without tls_bundle_id is operator-managed end-to-end: paths
	// from metadata still flow through unchanged so existing manual-TLS
	// deployments keep working.
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
		IngressSites: map[string]inventory.IngressSiteConfig{
			"manual": {
				Node:     "edge-1",
				Domains:  []string{"legacy.frameworks.network"},
				Kind:     "reverse_proxy_http",
				Upstream: "127.0.0.1:18099",
				Metadata: map[string]string{
					"tls_mode":      "files",
					"tls_cert_path": "/operator/legacy/legacy.crt",
					"tls_key_path":  "/operator/legacy/legacy.key",
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
	}, manifest, map[string]any{}, false, "", map[string]string{}, nil, nil)
	if err != nil {
		t.Fatalf("buildTaskConfig returned error: %v", err)
	}
	sites, ok := cfg.Metadata["proxy_sites"].([]map[string]any)
	if !ok || len(sites) != 1 {
		t.Fatalf("proxy_sites missing or wrong type: %#v", cfg.Metadata["proxy_sites"])
	}
	site := sites[0]
	if got := site["tls_cert_path"]; got != "/operator/legacy/legacy.crt" {
		t.Fatalf("tls_cert_path = %v; manual TLS paths must flow through for non-managed sites", got)
	}
	if got := site["tls_key_path"]; got != "/operator/legacy/legacy.key" {
		t.Fatalf("tls_key_path = %v", got)
	}
	if got := site["tls_mode"]; got != "files" {
		t.Fatalf("tls_mode = %v", got)
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
	}, manifest, map[string]any{}, false, "", map[string]string{}, nil, nil)
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

func TestPrivateerStaticPeersIncludeQuartermasterAcrossClusters(t *testing.T) {
	manifest := &inventory.Manifest{
		Clusters: map[string]inventory.ClusterConfig{
			"core":     {},
			"regional": {},
		},
		Hosts: map[string]inventory.Host{
			"central-1": {
				Cluster:            "core",
				ExternalIP:         "203.0.113.10",
				WireguardIP:        "10.88.0.10",
				WireguardPublicKey: "central-pub",
			},
			"regional-1": {
				Cluster:            "regional",
				ExternalIP:         "203.0.113.20",
				WireguardIP:        "10.88.1.20",
				WireguardPublicKey: "regional-1-pub",
			},
			"regional-2": {
				Cluster:            "regional",
				ExternalIP:         "203.0.113.21",
				WireguardIP:        "10.88.1.21",
				WireguardPublicKey: "regional-2-pub",
			},
			"us-1": {
				Cluster:            "us",
				ExternalIP:         "203.0.113.30",
				WireguardIP:        "10.88.2.30",
				WireguardPublicKey: "us-pub",
			},
		},
		Services: map[string]inventory.ServiceConfig{
			"quartermaster": {Enabled: true, Host: "central-1"},
			"privateer":     {Enabled: true},
		},
	}

	peers := buildPrivateerStaticPeers(manifest, "regional-1")
	got := map[string][]string{}
	for _, peer := range peers {
		name, _ := peer["name"].(string)
		allowed, _ := peer["allowed_ips"].([]string)
		got[name] = allowed
	}

	if _, ok := got["central-1"]; !ok {
		t.Fatalf("expected quartermaster host central-1 in privateer seed peers, got %#v", peers)
	}
	if got["central-1"][0] != "10.88.0.10/32" {
		t.Fatalf("central-1 allowed IPs = %v, want 10.88.0.10/32", got["central-1"])
	}
	if _, ok := got["regional-2"]; !ok {
		t.Fatalf("expected same-cluster peer regional-2, got %#v", peers)
	}
	if _, ok := got["us-1"]; ok {
		t.Fatalf("unexpected unrelated cross-cluster privateer peer us-1, got %#v", peers)
	}
}

func TestPrivateerStaticPeersIncludeReciprocalDependencyConsumers(t *testing.T) {
	manifest := &inventory.Manifest{
		Clusters: map[string]inventory.ClusterConfig{
			"core":  {},
			"media": {},
		},
		Hosts: map[string]inventory.Host{
			"central-1": {
				Cluster:            "core",
				ExternalIP:         "203.0.113.10",
				WireguardIP:        "10.88.0.10",
				WireguardPublicKey: "central-pub",
			},
			"media-1": {
				Cluster:            "media",
				ExternalIP:         "203.0.113.20",
				WireguardIP:        "10.88.1.20",
				WireguardPublicKey: "media-pub",
			},
		},
		Services: map[string]inventory.ServiceConfig{
			"quartermaster": {Enabled: true, Host: "central-1"},
			"chandler-us":   {Enabled: true, Deploy: "chandler", Cluster: "media", Host: "media-1"},
			"privateer":     {Enabled: true},
		},
	}

	peers := buildPrivateerStaticPeers(manifest, "central-1")
	got := map[string]struct{}{}
	for _, peer := range peers {
		name, _ := peer["name"].(string)
		got[name] = struct{}{}
	}

	if _, ok := got["media-1"]; !ok {
		t.Fatalf("expected provider central-1 to include reciprocal chandler consumer media-1, got %#v", peers)
	}
}

func TestPlannedProvisionHostsDedupesAndSorts(t *testing.T) {
	plan := &orchestrator.ExecutionPlan{
		AllTasks: []*orchestrator.Task{
			{Host: "regional-2"},
			{Host: "central-1"},
			{Host: "regional-2"},
			{Host: ""},
			nil,
		},
	}
	got := plannedProvisionHosts(plan)
	want := []string{"central-1", "regional-2"}
	if !slices.Equal(got, want) {
		t.Fatalf("plannedProvisionHosts = %v, want %v", got, want)
	}
}

func TestPrivateerSeedDNSUsesTopologyScopedAliases(t *testing.T) {
	manifest := &inventory.Manifest{
		Clusters: map[string]inventory.ClusterConfig{
			"core":     {},
			"regional": {Region: "eu-west"},
			"media-eu": {},
			"us":       {Region: "us-east"},
		},
		Hosts: map[string]inventory.Host{
			"central-1":  {Cluster: "core", WireguardIP: "10.88.0.10"},
			"central-2":  {Cluster: "core", WireguardIP: "10.88.0.11"},
			"regional-1": {Cluster: "regional", WireguardIP: "10.88.1.20"},
			"us-1":       {Cluster: "us", WireguardIP: "10.88.2.20"},
		},
		Services: map[string]inventory.ServiceConfig{
			"quartermaster": {Enabled: true, Host: "central-1"},
			"purser":        {Enabled: true, Host: "central-2"},
			"decklog-eu":    {Enabled: true, Deploy: "decklog", Host: "regional-1"},
			"decklog-us":    {Enabled: true, Deploy: "decklog", Host: "us-1"},
			"signalman":     {Enabled: true, Hosts: []string{"regional-1", "us-1"}},
			"bridge":        {Enabled: true, Host: "regional-1"},
			"chandler-eu":   {Enabled: true, Deploy: "chandler", Cluster: "media-eu", Host: "regional-1"},
			"foghorn-eu":    {Enabled: true, Deploy: "foghorn", Cluster: "media-eu", Host: "regional-1"},
		},
	}

	dns := buildPrivateerSeedDNS(manifest, "regional-1")
	if got := dns["quartermaster"]; len(got) != 1 || got[0] != "10.88.0.10" {
		t.Fatalf("quartermaster DNS = %v, want [10.88.0.10]", got)
	}
	if got := dns["purser"]; len(got) != 1 || got[0] != "10.88.0.11" {
		t.Fatalf("purser DNS = %v, want [10.88.0.11]", got)
	}
	if got := dns["decklog"]; len(got) != 1 || got[0] != "10.88.1.20" {
		t.Fatalf("decklog DNS = %v, want regional-local [10.88.1.20]", got)
	}
	if got := dns["chandler"]; len(got) != 1 || got[0] != "10.88.1.20" {
		t.Fatalf("chandler DNS = %v, want logical-cluster local [10.88.1.20]", got)
	}
	if got := dns["signalman.eu-west"]; len(got) != 1 || got[0] != "10.88.1.20" {
		t.Fatalf("signalman.eu-west DNS = %v, want [10.88.1.20]", got)
	}
	if got := dns["signalman.us-east"]; len(got) != 1 || got[0] != "10.88.2.20" {
		t.Fatalf("signalman.us-east DNS = %v, want [10.88.2.20]", got)
	}
	if got := dns["central-1"]; len(got) != 1 || got[0] != "10.88.0.10" {
		t.Fatalf("central-1 DNS = %v, want [10.88.0.10]", got)
	}
	if got := dns["central-2"]; len(got) != 1 || got[0] != "10.88.0.11" {
		t.Fatalf("central-2 DNS = %v, want [10.88.0.11]", got)
	}

	centralDNS := buildPrivateerSeedDNS(manifest, "central-1")
	if got, want := centralDNS["decklog"], []string{"10.88.1.20", "10.88.2.20"}; !slices.Equal(got, want) {
		t.Fatalf("central decklog DNS = %v, want global Decklog providers %v", got, want)
	}
}

func TestPrivateerSeedDNSIncludesGlobalSkipperBridgeAlias(t *testing.T) {
	manifest := &inventory.Manifest{
		Clusters: map[string]inventory.ClusterConfig{
			"core": {},
			"eu":   {},
			"us":   {},
		},
		Hosts: map[string]inventory.Host{
			"central-1": {Cluster: "core", WireguardIP: "10.88.0.10", WireguardPublicKey: "central-key"},
			"bridge-eu": {Cluster: "eu", WireguardIP: "10.88.1.20", WireguardPublicKey: "eu-key"},
			"bridge-us": {Cluster: "us", WireguardIP: "10.88.2.20", WireguardPublicKey: "us-key"},
		},
		Services: map[string]inventory.ServiceConfig{
			"quartermaster": {Enabled: true, Host: "central-1"},
			"skipper":       {Enabled: true, Host: "central-1"},
			"bridge-eu":     {Enabled: true, Deploy: "bridge", Cluster: "eu", Host: "bridge-eu"},
			"bridge-us":     {Enabled: true, Deploy: "bridge", Cluster: "us", Host: "bridge-us"},
		},
	}

	dns := buildPrivateerSeedDNS(manifest, "central-1")
	if got, want := dns["bridge"], []string{"10.88.1.20", "10.88.2.20"}; !slices.Equal(got, want) {
		t.Fatalf("bridge DNS = %v, want %v", got, want)
	}

	peers := privateerSeedPeerHosts(manifest, "central-1")
	for _, want := range []string{"bridge-eu", "bridge-us"} {
		if _, ok := peers[want]; !ok {
			t.Fatalf("central privateer peers missing %q in %v", want, sortedKeys(peers))
		}
	}

	for _, bridgeHost := range []string{"bridge-eu", "bridge-us"} {
		reciprocal := privateerSeedPeerHosts(manifest, bridgeHost)
		if _, ok := reciprocal["central-1"]; !ok {
			t.Fatalf("%s privateer peers missing central-1 in %v", bridgeHost, sortedKeys(reciprocal))
		}
	}
}

// TestResolveServiceDialNoSessionPrefersMeshIP locks the no-session fallback:
// when a Session is not in play (doctor / status callers, off-mesh provisioning
// with --no-tunnel in the future), service-to-service addressing must still
// prefer the mesh address over the public ExternalIP. The session path is
// covered by remoteaccess.Session tests.
func TestResolveServiceDialNoSessionPrefersMeshIP(t *testing.T) {
	manifest := &inventory.Manifest{
		Profile: "dev",
		Hosts: map[string]inventory.Host{
			"core-1": {ExternalIP: "203.0.113.5", WireguardIP: "10.88.0.2"},
		},
		Services: map[string]inventory.ServiceConfig{
			"quartermaster": {Enabled: true, Host: "core-1", GRPCPort: 19002},
		},
	}
	addr, serverName, _, err := resolveServiceDial(context.Background(), manifest, nil, "quartermaster", 19002)
	if err != nil {
		t.Fatalf("resolveServiceDial returned error: %v", err)
	}
	if addr != "10.88.0.2:19002" {
		t.Fatalf("addr = %q, want mesh address", addr)
	}
	if serverName != "" {
		t.Fatalf("serverName = %q, want empty (no-session direct dial relies on dial-address default)", serverName)
	}
}

// TestInternalCAFromRuntimeReturnsBootstrapPEM pins the wiring that feeds the
// inline internal CA into every operator-originated gRPC client (Quartermaster,
// Purser, Commodore) during bootstrap. If this returns empty when the bundle
// is staged, non-dev profiles fall back to the system trust store and TLS
// verification fails before the trust store is distributed.
func TestInternalCAFromRuntimeReturnsBootstrapPEM(t *testing.T) {
	const samplePEM = "-----BEGIN CERTIFICATE-----\n...\n-----END CERTIFICATE-----\n"
	got := internalCAFromRuntime(map[string]any{
		"internal_pki_bootstrap": &internalPKIBootstrap{CABundlePEM: samplePEM},
	})
	if got != samplePEM {
		t.Fatalf("internalCAFromRuntime = %q, want bootstrap PEM", got)
	}

	if internalCAFromRuntime(nil) != "" {
		t.Fatal("nil runtimeData should yield empty CA PEM")
	}
	if internalCAFromRuntime(map[string]any{}) != "" {
		t.Fatal("missing internal_pki_bootstrap key should yield empty CA PEM")
	}
	if internalCAFromRuntime(map[string]any{"internal_pki_bootstrap": (*internalPKIBootstrap)(nil)}) != "" {
		t.Fatal("nil bootstrap pointer should yield empty CA PEM")
	}
}

// TestBuildControlPlaneReportSurfacesQMResolutionFailureAsWarning pins
// the invariant: when Quartermaster cannot be resolved from the
// manifest, the report must carry Checked=true plus a warning, not the
// empty Checked=false that validateControlPlane's policy gate would
// read as success.
func TestBuildControlPlaneReportSurfacesQMResolutionFailureAsWarning(t *testing.T) {
	manifest := &inventory.Manifest{
		Profile: "dev",
		// No Services map → resolveServiceGRPCAddr fails for every name.
	}
	runtimeData := map[string]any{
		"system_tenant_id": "tenant-1",
		"service_token":    "secret",
	}

	report := buildControlPlaneReport(context.Background(), manifest, runtimeData, nil)

	if !report.Checked {
		t.Fatal("report.Checked must be true when resolution warnings exist; otherwise validateControlPlane silently passes")
	}
	var sawQMWarning bool
	for _, w := range report.Warnings {
		if w.Subject == "control-plane.quartermaster" && strings.Contains(w.Detail, "Could not resolve Quartermaster") {
			sawQMWarning = true
			break
		}
	}
	if !sawQMWarning {
		t.Fatalf("expected a Quartermaster resolution warning; got %+v", report.Warnings)
	}
}

// TestBuildControlPlaneReportSilencesOptionalResolutionFailuresWithoutSession
// locks the read-only-command policy: doctor and status (sess=nil) tolerate
// missing Commodore/Purser entries silently because those are optional for
// non-provisioning use. Only Quartermaster is mandatory.
//
// The context carries a tight deadline because ControlPlaneReadiness will
// build a Quartermaster gRPC client and invoke ListClusters; the unreachable
// test address would otherwise block on WaitForReady. Surface warnings come
// from the client connection failing inside the deadline, which is fine —
// this test only asserts on subjects that should be absent.
func TestBuildControlPlaneReportSilencesOptionalResolutionFailuresWithoutSession(t *testing.T) {
	manifest := &inventory.Manifest{
		Profile: "dev",
		Hosts: map[string]inventory.Host{
			"core-1": {ExternalIP: "10.0.0.1"},
		},
		Services: map[string]inventory.ServiceConfig{
			// Quartermaster present; Commodore and Purser intentionally absent.
			"quartermaster": {Enabled: true, Host: "core-1", GRPCPort: 19002},
		},
	}
	runtimeData := map[string]any{
		"system_tenant_id": "tenant-1",
		"service_token":    "secret",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	report := buildControlPlaneReport(ctx, manifest, runtimeData, nil)

	for _, w := range report.Warnings {
		if w.Subject == "control-plane.commodore" || w.Subject == "control-plane.purser" {
			t.Fatalf("read-only callers must not emit %s resolution warnings; got %+v", w.Subject, report.Warnings)
		}
	}
}

// TestEndpointResolutionWarningsPolicy pins the per-caller policy: read-only
// commands (sess=nil) only flag the mandatory Quartermaster endpoint, while
// provisioning (sess!=nil) flags every failure since each one will block a
// real downstream call.
func TestEndpointResolutionWarningsPolicy(t *testing.T) {
	qmErr := errors.New("qm resolution failed")
	commErr := errors.New("commodore resolution failed")
	purserErr := errors.New("purser resolution failed")

	noSession := endpointResolutionWarnings(nil, qmErr, commErr, purserErr)
	if len(noSession) != 1 {
		t.Fatalf("nil session should yield exactly 1 warning (QM only); got %d: %+v", len(noSession), noSession)
	}
	if noSession[0].Subject != "control-plane.quartermaster" {
		t.Fatalf("nil-session warning should be Quartermaster; got subject %q", noSession[0].Subject)
	}

	sess := &remoteaccess.Session{}
	withSession := endpointResolutionWarnings(sess, qmErr, commErr, purserErr)
	if len(withSession) != 3 {
		t.Fatalf("non-nil session should yield all 3 warnings; got %d: %+v", len(withSession), withSession)
	}

	// Nil errors must not produce warnings regardless of session.
	if got := endpointResolutionWarnings(sess, nil, nil, nil); len(got) != 0 {
		t.Fatalf("no errors should yield no warnings; got %+v", got)
	}
}

func TestBatchContainsServiceMatchesTaskType(t *testing.T) {
	batch := []*orchestrator.Task{
		{Name: "yugabyte-node-1", Type: "yugabyte", ServiceID: "postgres"},
	}
	if !batchContainsService(batch, "yugabyte") {
		t.Fatal("expected batchContainsService to match task type for Yugabyte nodes")
	}
}

// TestBuildServiceEnvVarsAggregatorPinnedServicesIgnoreHostRegion proves that
// periscope-ingest, purser, periscope-query, and commodore bind aggregator
// Kafka regardless of which region's host they're deployed on. Pinning here
// prevents a regional host placement from dual-writing central ClickHouse or
// missing centralized billing rows.
func TestBuildServiceEnvVarsAggregatorPinnedServicesIgnoreHostRegion(t *testing.T) {
	manifest := threeRegionKafkaManifest()

	pinned := []string{"periscope-ingest", "purser", "periscope-query", "commodore"}
	for _, svc := range pinned {
		for _, host := range []string{"regional-eu-1", "regional-us-1", "regional-ap-1"} {
			task := &orchestrator.Task{Type: svc, ServiceID: svc, Host: host}
			env, err := buildServiceEnvVars(task, manifest, map[string]any{}, "", "", testLoadSharedEnv(t, manifest), nil)
			if err != nil {
				t.Fatalf("buildServiceEnvVars(%s on %s): %v", svc, host, err)
			}
			if env["KAFKA_CLUSTER_ID"] != "eu-kafka" {
				t.Fatalf("%s on %s: KAFKA_CLUSTER_ID = %q, want eu-kafka (aggregator)", svc, host, env["KAFKA_CLUSTER_ID"])
			}
			if !strings.Contains(env["KAFKA_BROKERS"], "regional-eu-1") {
				t.Fatalf("%s on %s: KAFKA_BROKERS = %q, want aggregator brokers", svc, host, env["KAFKA_BROKERS"])
			}
		}
	}
}

// TestBuildServiceEnvVarsSignalmanPerInstanceKafkaIdentity proves the
// provisioner-owned per-replica group/client/reset env: each signalman host
// gets a unique stable KAFKA_GROUP_ID derived from the hostname, and reset
// is forced to latest so a fresh group doesn't replay retained history to
// live clients.
func TestBuildServiceEnvVarsSignalmanPerInstanceKafkaIdentity(t *testing.T) {
	manifest := threeRegionKafkaManifest()

	envByHost := map[string]map[string]string{}
	for _, host := range []string{"regional-us-1", "regional-us-2", "regional-us-3"} {
		task := &orchestrator.Task{Type: "signalman", ServiceID: "signalman", Host: host}
		env, err := buildServiceEnvVars(task, manifest, map[string]any{}, "", "", testLoadSharedEnv(t, manifest), nil)
		if err != nil {
			t.Fatalf("buildServiceEnvVars signalman on %s: %v", host, err)
		}
		envByHost[host] = env
	}

	seenGroups := map[string]string{}
	for host, env := range envByHost {
		want := "signalman-" + host
		if env["KAFKA_GROUP_ID"] != want {
			t.Errorf("%s: KAFKA_GROUP_ID = %q, want %q", host, env["KAFKA_GROUP_ID"], want)
		}
		if env["KAFKA_CLIENT_ID"] != want {
			t.Errorf("%s: KAFKA_CLIENT_ID = %q, want %q", host, env["KAFKA_CLIENT_ID"], want)
		}
		if env["KAFKA_CONSUME_RESET_OFFSET"] != "latest" {
			t.Errorf("%s: KAFKA_CONSUME_RESET_OFFSET = %q, want latest", host, env["KAFKA_CONSUME_RESET_OFFSET"])
		}
		if prevHost, dup := seenGroups[env["KAFKA_GROUP_ID"]]; dup {
			t.Errorf("KAFKA_GROUP_ID collision: %s and %s both → %s", host, prevHost, env["KAFKA_GROUP_ID"])
		}
		seenGroups[env["KAFKA_GROUP_ID"]] = host
	}

	// Stability across reruns: same host, same env.
	again, err := buildServiceEnvVars(&orchestrator.Task{Type: "signalman", ServiceID: "signalman", Host: "regional-us-1"}, manifest, map[string]any{}, "", "", testLoadSharedEnv(t, manifest), nil)
	if err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if again["KAFKA_GROUP_ID"] != envByHost["regional-us-1"]["KAFKA_GROUP_ID"] {
		t.Fatalf("signalman group not stable across reruns")
	}
}

// TestBuildServiceEnvVarsBridgeMultiTargetSignalman proves the bridge env
// carries every Signalman replica in its region (SIGNALMAN_GRPC_ADDRS) plus
// the full topology map (SIGNALMAN_GRPC_ADDRS_BY_REGION).
func TestBuildServiceEnvVarsBridgeMultiTargetSignalman(t *testing.T) {
	manifest := threeRegionKafkaManifest()

	task := &orchestrator.Task{Type: "bridge", ServiceID: "bridge", Host: "regional-eu-1"}
	env, err := buildServiceEnvVars(task, manifest, map[string]any{}, "", "", testLoadSharedEnv(t, manifest), nil)
	if err != nil {
		t.Fatalf("buildServiceEnvVars bridge: %v", err)
	}

	if got := env["SIGNALMAN_GRPC_ADDR"]; got != "signalman.internal:19005" {
		t.Fatalf("SIGNALMAN_GRPC_ADDR = %q, want service DNS", got)
	}
	if got := env["SIGNALMAN_GRPC_ADDRS"]; got != "" {
		t.Fatalf("SIGNALMAN_GRPC_ADDRS = %q, want unset; local placement belongs to service DNS", got)
	}

	byRegion := env["SIGNALMAN_GRPC_ADDRS_BY_REGION"]
	if byRegion == "" {
		t.Fatal("SIGNALMAN_GRPC_ADDRS_BY_REGION missing")
	}
	for _, region := range []string{"eu-west", "us-east", "ap-south"} {
		if !strings.Contains(byRegion, region+"=") {
			t.Errorf("SIGNALMAN_GRPC_ADDRS_BY_REGION missing region %s: %q", region, byRegion)
		}
		if !strings.Contains(byRegion, "signalman."+region+".internal:19005") {
			t.Errorf("SIGNALMAN_GRPC_ADDRS_BY_REGION missing service alias for %s: %q", region, byRegion)
		}
	}
	if strings.Contains(byRegion, "regional-us-1") || strings.Contains(byRegion, "regional-eu-1") {
		t.Errorf("SIGNALMAN_GRPC_ADDRS_BY_REGION should not expose concrete node names: %q", byRegion)
	}
}

func TestBuildServiceEnvVarsBridgeRegionalHostIncludesControlPlaneGRPCDeps(t *testing.T) {
	manifest := threeRegionKafkaManifest()
	manifest.Hosts["central-eu-1"] = inventory.Host{WireguardIP: "10.88.10.10", Labels: map[string]string{"region": "eu-west"}}
	for _, svc := range []string{"bridge", "commodore", "quartermaster", "purser", "periscope-query", "decklog"} {
		manifest.Services[svc] = inventory.ServiceConfig{Enabled: true, Host: "central-eu-1"}
	}

	task := &orchestrator.Task{Type: "bridge", ServiceID: "bridge", Host: "regional-eu-2", ClusterID: "eu-kafka"}
	env, err := buildServiceEnvVars(task, manifest, map[string]any{}, "", "", testLoadSharedEnv(t, manifest), nil)
	if err != nil {
		t.Fatalf("buildServiceEnvVars bridge: %v", err)
	}

	for _, key := range []string{
		"COMMODORE_GRPC_ADDR",
		"PERISCOPE_GRPC_ADDR",
		"PURSER_GRPC_ADDR",
		"QUARTERMASTER_GRPC_ADDR",
		"SIGNALMAN_GRPC_ADDR",
		"DECKLOG_GRPC_ADDR",
	} {
		if env[key] == "" {
			t.Fatalf("%s missing from regional bridge env", key)
		}
	}
}

func TestMissingRequiredGeneratedEnvBridgeRequiresEnabledDeps(t *testing.T) {
	manifest := threeRegionKafkaManifest()
	manifest.Profile = "production"
	manifest.Services["bridge"] = inventory.ServiceConfig{Enabled: true, Host: "regional-eu-2"}
	for _, svc := range []string{"commodore", "periscope-query", "purser", "quartermaster", "decklog"} {
		manifest.Services[svc] = inventory.ServiceConfig{Enabled: true, Host: "regional-eu-1"}
	}

	missing := missingRequiredGeneratedEnv(manifest, "bridge", map[string]string{})
	if !slices.Contains(missing, "COMMODORE_GRPC_ADDR") {
		t.Fatalf("missing generated env = %v, want COMMODORE_GRPC_ADDR", missing)
	}
}

// threeRegionKafkaManifest builds an EU(aggregator)+US+AP-south topology with
// 3 signalman replicas per region — the shape this hardening pass is designed
// to support before the APAC media cell actually ships.
func threeRegionKafkaManifest() *inventory.Manifest {
	mk := func(ip string, region string) inventory.Host {
		return inventory.Host{WireguardIP: ip, Labels: map[string]string{"region": region}}
	}
	return &inventory.Manifest{
		Profile:    "dev",
		RootDomain: "frameworks.network",
		Hosts: map[string]inventory.Host{
			"regional-eu-1": mk("10.88.0.11", "eu-west"),
			"regional-eu-2": mk("10.88.0.12", "eu-west"),
			"regional-eu-3": mk("10.88.0.13", "eu-west"),
			"regional-us-1": mk("10.88.1.11", "us-east"),
			"regional-us-2": mk("10.88.1.12", "us-east"),
			"regional-us-3": mk("10.88.1.13", "us-east"),
			"regional-ap-1": mk("10.88.2.11", "ap-south"),
			"regional-ap-2": mk("10.88.2.12", "ap-south"),
			"regional-ap-3": mk("10.88.2.13", "ap-south"),
		},
		Services: map[string]inventory.ServiceConfig{
			"signalman": {
				Enabled: true,
				Hosts: []string{
					"regional-eu-1", "regional-eu-2", "regional-eu-3",
					"regional-us-1", "regional-us-2", "regional-us-3",
					"regional-ap-1", "regional-ap-2", "regional-ap-3",
				},
			},
		},
		Infrastructure: inventory.InfrastructureConfig{
			Kafka: &inventory.KafkaConfig{
				Enabled:   true,
				ClusterID: "eu-kafka",
				RegionID:  "eu-west",
				Role:      "aggregator",
				Brokers: []inventory.KafkaBroker{
					{Host: "regional-eu-1", ID: 1, Port: 9092},
					{Host: "regional-eu-2", ID: 2, Port: 9092},
					{Host: "regional-eu-3", ID: 3, Port: 9092},
				},
				Regional: []inventory.RegionalKafkaCluster{
					{
						RegionID:  "us-east",
						Role:      "regional",
						ClusterID: "us-kafka",
						Brokers: []inventory.KafkaBroker{
							{Host: "regional-us-1", ID: 11, Port: 9092},
							{Host: "regional-us-2", ID: 12, Port: 9092},
							{Host: "regional-us-3", ID: 13, Port: 9092},
						},
					},
					{
						RegionID:  "ap-south",
						Role:      "regional",
						ClusterID: "ap-kafka",
						Brokers: []inventory.KafkaBroker{
							{Host: "regional-ap-1", ID: 21, Port: 9092},
							{Host: "regional-ap-2", ID: 22, Port: 9092},
							{Host: "regional-ap-3", ID: 23, Port: 9092},
						},
					},
				},
			},
		},
	}
}
