package grpc

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "frameworks/pkg/proto"
)

func strPtr(s string) *string { return &s }

func TestUpdateTenantCluster(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name      string
		req       *pb.UpdateTenantClusterRequest
		setupMock func(sqlmock.Sqlmock)
		assert    func(*testing.T, error)
	}{
		{
			name: "missing_tenant_id",
			req:  &pb.UpdateTenantClusterRequest{TenantId: ""},
			assert: func(t *testing.T, err error) {
				assertGRPCCode(t, err, codes.InvalidArgument)
			},
		},
		{
			name: "no_fields_to_update",
			req:  &pb.UpdateTenantClusterRequest{TenantId: "tenant-1"},
			assert: func(t *testing.T, err error) {
				assertGRPCCode(t, err, codes.InvalidArgument)
			},
		},
		{
			name: "active_subscription_allowed",
			req:  &pb.UpdateTenantClusterRequest{TenantId: "tenant-1", PrimaryClusterId: strPtr("cluster-new")},
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery("SELECT primary_cluster_id FROM quartermaster.tenants").
					WithArgs("tenant-1").
					WillReturnRows(sqlmock.NewRows([]string{"primary_cluster_id"}).AddRow("cluster-old"))
				mock.ExpectQuery("SELECT EXISTS").
					WithArgs("tenant-1", "cluster-new").
					WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
				mock.ExpectExec("UPDATE quartermaster.tenants SET").
					WithArgs("cluster-new", "tenant-1").
					WillReturnResult(sqlmock.NewResult(0, 1))
			},
			assert: func(t *testing.T, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			},
		},
		{
			name: "pending_subscription_denied",
			req:  &pb.UpdateTenantClusterRequest{TenantId: "tenant-1", PrimaryClusterId: strPtr("cluster-pending")},
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery("SELECT primary_cluster_id FROM quartermaster.tenants").
					WithArgs("tenant-1").
					WillReturnRows(sqlmock.NewRows([]string{"primary_cluster_id"}).AddRow("cluster-old"))
				mock.ExpectQuery("SELECT EXISTS").
					WithArgs("tenant-1", "cluster-pending").
					WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
			},
			assert: func(t *testing.T, err error) {
				assertGRPCCode(t, err, codes.FailedPrecondition)
			},
		},
		{
			name: "tenant_not_found_on_update",
			req:  &pb.UpdateTenantClusterRequest{TenantId: "tenant-gone", PrimaryClusterId: strPtr("cluster-new")},
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery("SELECT primary_cluster_id FROM quartermaster.tenants").
					WithArgs("tenant-gone").
					WillReturnRows(sqlmock.NewRows([]string{"primary_cluster_id"}).AddRow("cluster-old"))
				mock.ExpectQuery("SELECT EXISTS").
					WithArgs("tenant-gone", "cluster-new").
					WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
				mock.ExpectExec("UPDATE quartermaster.tenants SET").
					WithArgs("cluster-new", "tenant-gone").
					WillReturnResult(sqlmock.NewResult(0, 0))
			},
			assert: func(t *testing.T, err error) {
				assertGRPCCode(t, err, codes.NotFound)
			},
		},
		{
			name: "db_error_on_access_check",
			req:  &pb.UpdateTenantClusterRequest{TenantId: "tenant-1", PrimaryClusterId: strPtr("cluster-new")},
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery("SELECT primary_cluster_id FROM quartermaster.tenants").
					WithArgs("tenant-1").
					WillReturnRows(sqlmock.NewRows([]string{"primary_cluster_id"}).AddRow("cluster-old"))
				mock.ExpectQuery("SELECT EXISTS").
					WithArgs("tenant-1", "cluster-new").
					WillReturnError(fmt.Errorf("connection refused"))
			},
			assert: func(t *testing.T, err error) {
				assertGRPCCode(t, err, codes.Internal)
			},
		},
		{
			name: "db_error_on_update",
			req:  &pb.UpdateTenantClusterRequest{TenantId: "tenant-1", PrimaryClusterId: strPtr("cluster-new")},
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery("SELECT primary_cluster_id FROM quartermaster.tenants").
					WithArgs("tenant-1").
					WillReturnRows(sqlmock.NewRows([]string{"primary_cluster_id"}).AddRow("cluster-old"))
				mock.ExpectQuery("SELECT EXISTS").
					WithArgs("tenant-1", "cluster-new").
					WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
				mock.ExpectExec("UPDATE quartermaster.tenants SET").
					WithArgs("cluster-new", "tenant-1").
					WillReturnError(fmt.Errorf("connection refused"))
			},
			assert: func(t *testing.T, err error) {
				assertGRPCCode(t, err, codes.Internal)
			},
		},
		{
			name: "update_deployment_model_only",
			req:  &pb.UpdateTenantClusterRequest{TenantId: "tenant-1", DeploymentModel: strPtr("dedicated")},
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectExec("UPDATE quartermaster.tenants SET").
					WithArgs("dedicated", "tenant-1").
					WillReturnResult(sqlmock.NewResult(0, 1))
			},
			assert: func(t *testing.T, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var db *sql.DB
			var mock sqlmock.Sqlmock
			if test.setupMock != nil {
				var err error
				db, mock, err = sqlmock.New()
				if err != nil {
					t.Fatalf("failed to create sqlmock: %v", err)
				}
				defer db.Close()
				test.setupMock(mock)
			}

			server := &QuartermasterServer{db: db, logger: logrus.New()}
			_, err := server.UpdateTenantCluster(ctx, test.req)
			test.assert(t, err)

			if test.setupMock != nil {
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatalf("unmet expectations: %v", err)
				}
			}
		})
	}
}

func TestGetClusterRouting(t *testing.T) {
	ctx := context.Background()

	// Reusable row builders for the multi-query flow
	clusterCols := []string{
		"cluster_id", "cluster_name", "cluster_type", "base_url",
		"kafka_brokers", "database_url", "periscope_url", "topic_prefix",
		"max_concurrent_streams", "current_stream_count", "health_status",
	}
	foghornCols := []string{"advertise_host", "port"}
	peerCols := []string{"cluster_id", "cluster_name", "cluster_type", "base_url", "s3_bucket", "s3_endpoint", "s3_region", "foghorn_advertise_host", "foghorn_port"}

	tests := []struct {
		name      string
		req       *pb.GetClusterRoutingRequest
		setupMock func(sqlmock.Sqlmock)
		assert    func(*testing.T, *pb.ClusterRoutingResponse, error)
	}{
		{
			name: "missing_tenant_id",
			req:  &pb.GetClusterRoutingRequest{TenantId: ""},
			assert: func(t *testing.T, resp *pb.ClusterRoutingResponse, err error) {
				assertGRPCCode(t, err, codes.InvalidArgument)
			},
		},
		{
			name: "tenant_not_found",
			req:  &pb.GetClusterRoutingRequest{TenantId: "tenant-gone"},
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery("FROM quartermaster.tenants").
					WithArgs("tenant-gone").
					WillReturnError(sql.ErrNoRows)
			},
			assert: func(t *testing.T, resp *pb.ClusterRoutingResponse, err error) {
				assertGRPCCode(t, err, codes.NotFound)
			},
		},
		{
			name: "db_error_on_tenant",
			req:  &pb.GetClusterRoutingRequest{TenantId: "tenant-1"},
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery("FROM quartermaster.tenants").
					WithArgs("tenant-1").
					WillReturnError(fmt.Errorf("connection refused"))
			},
			assert: func(t *testing.T, resp *pb.ClusterRoutingResponse, err error) {
				assertGRPCCode(t, err, codes.Internal)
			},
		},
		{
			name: "capacity_exceeded",
			req:  &pb.GetClusterRoutingRequest{TenantId: "tenant-1"},
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery("FROM quartermaster.tenants").
					WithArgs("tenant-1").
					WillReturnRows(sqlmock.NewRows([]string{"primary_cluster_id", "official_cluster_id", "deployment_tier"}).
						AddRow("cluster-full", "", "pro"))
				mock.ExpectQuery("FROM quartermaster.infrastructure_clusters").
					WithArgs("cluster-full", "tenant-1", int32(0)).
					WillReturnError(sql.ErrNoRows)
			},
			assert: func(t *testing.T, resp *pb.ClusterRoutingResponse, err error) {
				assertGRPCCode(t, err, codes.NotFound)
			},
		},
		{
			name: "happy_path",
			req:  &pb.GetClusterRoutingRequest{TenantId: "tenant-1"},
			setupMock: func(mock sqlmock.Sqlmock) {
				// 1. Tenant lookup
				mock.ExpectQuery("FROM quartermaster.tenants").
					WithArgs("tenant-1").
					WillReturnRows(sqlmock.NewRows([]string{"primary_cluster_id", "official_cluster_id", "deployment_tier"}).
						AddRow("cluster-1", "", "pro"))
				// 2. Cluster routing with capacity check
				mock.ExpectQuery("FROM quartermaster.infrastructure_clusters").
					WithArgs("cluster-1", "tenant-1", int32(0)).
					WillReturnRows(sqlmock.NewRows(clusterCols).
						AddRow("cluster-1", "Primary Cluster", "shared-community", "frameworks.cloud",
							pq.StringArray{"broker:9092"}, nil, nil, "tenant1_",
							int32(100), int32(5), "healthy"))
				// 3. Tenant resource limits
				mock.ExpectQuery("FROM quartermaster.tenant_cluster_access").
					WithArgs("tenant-1", "cluster-1").
					WillReturnError(sql.ErrNoRows)
				// 4. Foghorn address
				mock.ExpectQuery("FROM quartermaster.foghorn_cluster_assignments").
					WithArgs("cluster-1").
					WillReturnRows(sqlmock.NewRows(foghornCols).AddRow("foghorn.cluster-1", int32(50051)))
				// 5. No official cluster (same as primary), skip official lookup
				// 6. Cluster peers
				mock.ExpectQuery("FROM quartermaster.tenant_cluster_access tca").
					WithArgs("tenant-1").
					WillReturnRows(sqlmock.NewRows(peerCols).
						AddRow("cluster-1", "Primary Cluster", "shared-community", "frameworks.cloud", "", "", "", "foghorn.cluster-1", int32(50051)))
			},
			assert: func(t *testing.T, resp *pb.ClusterRoutingResponse, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if resp.ClusterId != "cluster-1" {
					t.Fatalf("expected cluster-1, got %q", resp.ClusterId)
				}
				if resp.ClusterName != "Primary Cluster" {
					t.Fatalf("expected 'Primary Cluster', got %q", resp.ClusterName)
				}
				if resp.GetFoghornGrpcAddr() != "foghorn.cluster-1:50051" {
					t.Fatalf("expected foghorn addr, got %q", resp.GetFoghornGrpcAddr())
				}
				if len(resp.KafkaBrokers) != 1 || resp.KafkaBrokers[0] != "broker:9092" {
					t.Fatalf("unexpected kafka brokers: %v", resp.KafkaBrokers)
				}
				if len(resp.ClusterPeers) != 1 {
					t.Fatalf("expected 1 peer, got %d", len(resp.ClusterPeers))
				}
				if resp.ClusterPeers[0].Role != "preferred" {
					t.Fatalf("expected role 'preferred', got %q", resp.ClusterPeers[0].Role)
				}
			},
		},
		{
			name: "official_cluster_populated",
			req:  &pb.GetClusterRoutingRequest{TenantId: "tenant-1"},
			setupMock: func(mock sqlmock.Sqlmock) {
				// 1. Tenant lookup — official differs from primary
				mock.ExpectQuery("FROM quartermaster.tenants").
					WithArgs("tenant-1").
					WillReturnRows(sqlmock.NewRows([]string{"primary_cluster_id", "official_cluster_id", "deployment_tier"}).
						AddRow("cluster-eu", "cluster-us", "pro"))
				// 2. Cluster routing
				mock.ExpectQuery("FROM quartermaster.infrastructure_clusters").
					WithArgs("cluster-eu", "tenant-1", int32(0)).
					WillReturnRows(sqlmock.NewRows(clusterCols).
						AddRow("cluster-eu", "EU Cluster", "shared-community", "eu.frameworks.cloud",
							pq.StringArray{"broker:9092"}, nil, nil, "",
							int32(0), int32(0), "healthy"))
				// 3. Tenant resource limits
				mock.ExpectQuery("FROM quartermaster.tenant_cluster_access").
					WithArgs("tenant-1", "cluster-eu").
					WillReturnError(sql.ErrNoRows)
				// 4. Foghorn address (primary)
				mock.ExpectQuery("FROM quartermaster.foghorn_cluster_assignments").
					WithArgs("cluster-eu").
					WillReturnRows(sqlmock.NewRows(foghornCols).AddRow("foghorn.eu", int32(50051)))
				// 5. Official cluster info
				mock.ExpectQuery("FROM quartermaster.infrastructure_clusters").
					WithArgs("cluster-us").
					WillReturnRows(sqlmock.NewRows([]string{"cluster_name", "base_url"}).
						AddRow("US Cluster", "us.frameworks.cloud"))
				// 6. Official foghorn address
				mock.ExpectQuery("FROM quartermaster.foghorn_cluster_assignments").
					WithArgs("cluster-us").
					WillReturnRows(sqlmock.NewRows(foghornCols).AddRow("foghorn.us", int32(50051)))
				// 7. Cluster peers
				mock.ExpectQuery("FROM quartermaster.tenant_cluster_access tca").
					WithArgs("tenant-1").
					WillReturnRows(sqlmock.NewRows(peerCols).
						AddRow("cluster-eu", "EU Cluster", "shared-community", "eu.frameworks.cloud", "", "", "", "foghorn.eu", int32(50051)).
						AddRow("cluster-us", "US Cluster", "shared-community", "us.frameworks.cloud", "", "", "", "foghorn.us", int32(50051)))
			},
			assert: func(t *testing.T, resp *pb.ClusterRoutingResponse, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if resp.ClusterId != "cluster-eu" {
					t.Fatalf("expected cluster-eu, got %q", resp.ClusterId)
				}
				if resp.GetOfficialClusterId() != "cluster-us" {
					t.Fatalf("expected official cluster-us, got %q", resp.GetOfficialClusterId())
				}
				if resp.GetOfficialBaseUrl() != "us.frameworks.cloud" {
					t.Fatalf("expected official base_url, got %q", resp.GetOfficialBaseUrl())
				}
				if resp.GetOfficialFoghornGrpcAddr() != "foghorn.us:50051" {
					t.Fatalf("expected official foghorn addr, got %q", resp.GetOfficialFoghornGrpcAddr())
				}
				// Verify peer foghorn addresses are populated
				for _, p := range resp.ClusterPeers {
					if p.FoghornGrpcAddr == "" {
						t.Fatalf("expected foghorn_grpc_addr for peer %s", p.ClusterId)
					}
				}
			},
		},
		{
			name: "cluster_peers_roles",
			req:  &pb.GetClusterRoutingRequest{TenantId: "tenant-1"},
			setupMock: func(mock sqlmock.Sqlmock) {
				// 1. Tenant lookup — has both primary and official
				mock.ExpectQuery("FROM quartermaster.tenants").
					WithArgs("tenant-1").
					WillReturnRows(sqlmock.NewRows([]string{"primary_cluster_id", "official_cluster_id", "deployment_tier"}).
						AddRow("cluster-eu", "cluster-us", "pro"))
				// 2. Cluster routing
				mock.ExpectQuery("FROM quartermaster.infrastructure_clusters").
					WithArgs("cluster-eu", "tenant-1", int32(0)).
					WillReturnRows(sqlmock.NewRows(clusterCols).
						AddRow("cluster-eu", "EU Cluster", "shared-community", "eu.frameworks.cloud",
							pq.StringArray{}, nil, nil, "",
							int32(0), int32(0), "healthy"))
				// 3. Tenant resource limits
				mock.ExpectQuery("FROM quartermaster.tenant_cluster_access").
					WithArgs("tenant-1", "cluster-eu").
					WillReturnError(sql.ErrNoRows)
				// 4. Foghorn address
				mock.ExpectQuery("FROM quartermaster.foghorn_cluster_assignments").
					WithArgs("cluster-eu").
					WillReturnRows(sqlmock.NewRows(foghornCols))
				// 5. Official cluster
				mock.ExpectQuery("FROM quartermaster.infrastructure_clusters").
					WithArgs("cluster-us").
					WillReturnRows(sqlmock.NewRows([]string{"cluster_name", "base_url"}).
						AddRow("US Cluster", "us.frameworks.cloud"))
				// 6. Official foghorn
				mock.ExpectQuery("FROM quartermaster.foghorn_cluster_assignments").
					WithArgs("cluster-us").
					WillReturnRows(sqlmock.NewRows(foghornCols))
				// 7. Three peers: preferred, official, subscribed
				mock.ExpectQuery("FROM quartermaster.tenant_cluster_access tca").
					WithArgs("tenant-1").
					WillReturnRows(sqlmock.NewRows(peerCols).
						AddRow("cluster-eu", "EU Cluster", "shared-community", "eu.frameworks.cloud", "", "", "", "foghorn.eu", int32(50051)).
						AddRow("cluster-us", "US Cluster", "shared-community", "us.frameworks.cloud", "", "", "", "foghorn.us", int32(50051)).
						AddRow("cluster-ap", "AP Cluster", "shared-community", "ap.frameworks.cloud", "", "", "", "foghorn.ap", int32(50051)))
			},
			assert: func(t *testing.T, resp *pb.ClusterRoutingResponse, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if len(resp.ClusterPeers) != 3 {
					t.Fatalf("expected 3 peers, got %d", len(resp.ClusterPeers))
				}
				roles := map[string]string{}
				for _, p := range resp.ClusterPeers {
					roles[p.ClusterId] = p.Role
				}
				if roles["cluster-eu"] != "preferred" {
					t.Fatalf("expected cluster-eu role 'preferred', got %q", roles["cluster-eu"])
				}
				if roles["cluster-us"] != "official" {
					t.Fatalf("expected cluster-us role 'official', got %q", roles["cluster-us"])
				}
				if roles["cluster-ap"] != "subscribed" {
					t.Fatalf("expected cluster-ap role 'subscribed', got %q", roles["cluster-ap"])
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var db *sql.DB
			var mock sqlmock.Sqlmock
			if test.setupMock != nil {
				var err error
				db, mock, err = sqlmock.New()
				if err != nil {
					t.Fatalf("failed to create sqlmock: %v", err)
				}
				defer db.Close()
				test.setupMock(mock)
			}

			server := &QuartermasterServer{db: db, logger: logrus.New()}
			resp, err := server.GetClusterRouting(ctx, test.req)
			test.assert(t, resp, err)

			if test.setupMock != nil {
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatalf("unmet expectations: %v", err)
				}
			}
		})
	}
}

func assertGRPCCode(t *testing.T, err error, expected codes.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error with code %v, got nil", expected)
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %v", err)
	}
	if st.Code() != expected {
		t.Fatalf("expected code %v, got %v: %s", expected, st.Code(), st.Message())
	}
}

func TestListPeers_UsesFoghornClusterAssignments(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{"cluster_id", "shared_tenant_ids", "cluster_name", "cluster_type", "foghorn_addr"}).
		AddRow("peer-cluster", pq.Array([]string{"tenant-a"}), "Peer Cluster", "shared-lb", "foghorn.peer.example.com:18019")

	mock.ExpectQuery("(?s)WITH my_tenants AS.*foghorn_cluster_assignments").
		WithArgs("local-cluster").
		WillReturnRows(rows)

	server := &QuartermasterServer{db: db, logger: logrus.New()}
	resp, err := server.ListPeers(context.Background(), &pb.ListPeersRequest{ClusterId: "local-cluster"})
	if err != nil {
		t.Fatalf("ListPeers returned error: %v", err)
	}
	if len(resp.Peers) != 1 {
		t.Fatalf("expected 1 peer, got %d", len(resp.Peers))
	}
	if resp.Peers[0].GetFoghornAddr() != "foghorn.peer.example.com:18019" {
		t.Fatalf("unexpected foghorn addr: %s", resp.Peers[0].GetFoghornAddr())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestGetClusterRouting_FormatsIPv6FoghornAddresses(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	clusterCols := []string{
		"cluster_id", "cluster_name", "cluster_type", "base_url",
		"kafka_brokers", "database_url", "periscope_url", "topic_prefix",
		"max_concurrent_streams", "current_stream_count", "health_status",
	}
	peerCols := []string{"cluster_id", "cluster_name", "cluster_type", "base_url", "s3_bucket", "s3_endpoint", "s3_region", "foghorn_advertise_host", "foghorn_port"}

	mock.ExpectQuery("FROM quartermaster.tenants").
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"primary_cluster_id", "official_cluster_id", "deployment_tier"}).
			AddRow("cluster-v6", "", "pro"))
	mock.ExpectQuery("FROM quartermaster.infrastructure_clusters").
		WithArgs("cluster-v6", "tenant-1", int32(0)).
		WillReturnRows(sqlmock.NewRows(clusterCols).
			AddRow("cluster-v6", "IPv6 Cluster", "shared-community", "v6.frameworks.cloud", pq.StringArray{}, nil, nil, "", int32(0), int32(0), "healthy"))
	mock.ExpectQuery("FROM quartermaster.tenant_cluster_access").
		WithArgs("tenant-1", "cluster-v6").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("FROM quartermaster.foghorn_cluster_assignments").
		WithArgs("cluster-v6").
		WillReturnRows(sqlmock.NewRows([]string{"advertise_host", "port"}).AddRow("2001:db8::10", int32(50051)))
	mock.ExpectQuery("FROM quartermaster.tenant_cluster_access tca").
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows(peerCols).
			AddRow("cluster-v6", "IPv6 Cluster", "shared-community", "v6.frameworks.cloud", "", "", "", "2001:db8::10", int32(50051)))

	server := &QuartermasterServer{db: db, logger: logrus.New()}
	resp, err := server.GetClusterRouting(context.Background(), &pb.GetClusterRoutingRequest{TenantId: "tenant-1"})
	if err != nil {
		t.Fatalf("GetClusterRouting returned error: %v", err)
	}
	if resp.GetFoghornGrpcAddr() != "[2001:db8::10]:50051" {
		t.Fatalf("unexpected primary foghorn addr: %s", resp.GetFoghornGrpcAddr())
	}
	if len(resp.GetClusterPeers()) != 1 || resp.GetClusterPeers()[0].GetFoghornGrpcAddr() != "[2001:db8::10]:50051" {
		t.Fatalf("unexpected peer foghorn addr: %+v", resp.GetClusterPeers())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
