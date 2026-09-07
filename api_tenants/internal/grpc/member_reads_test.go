package grpc

import (
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/lib/pq"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestGetTenantMemberContractRedactsPrivateInfrastructure(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Now().UTC()
	columns := []string{
		"id", "name", "subdomain", "custom_domain", "logo_url", "primary_color", "secondary_color",
		"deployment_tier", "custom_subdomain_enabled", "custom_domain_enabled", "billing_entitlements_observed", "deployment_model",
		"primary_cluster_id", "official_cluster_id", "kafka_topic_prefix", "kafka_brokers", "database_url",
		"is_active", "monitoring_enabled", "created_at", "updated_at", "rate_limit_per_minute", "rate_limit_burst",
	}
	mock.ExpectQuery("FROM quartermaster.tenants").WithArgs("tenant-a").WillReturnRows(
		sqlmock.NewRows(columns).AddRow(
			"tenant-a", "Tenant A", "tenant-a", "video.example.com", nil, "#000", "#fff",
			"production", true, true, true, "shared", "private-edge", "official-cell", "tenant_a",
			pq.Array([]string{"kafka.internal:9092"}), "postgres://secret", true, true, now, now, 60, 10,
		),
	)

	resp, err := (&QuartermasterServer{db: db, logger: logrus.New()}).GetTenant(
		tenantCtx("tenant-a", "member"), &quartermasterpb.GetTenantRequest{TenantId: "tenant-a"},
	)
	if err != nil {
		t.Fatalf("member tenant read failed: %v", err)
	}
	tenant := resp.GetTenant()
	if tenant.GetName() != "Tenant A" || tenant.GetSubdomain() != "tenant-a" || !tenant.GetCustomSubdomainEnabled() {
		t.Fatalf("member-safe tenant fields missing: %+v", tenant)
	}
	if tenant.PrimaryClusterId != nil || tenant.OfficialClusterId != nil || tenant.DatabaseUrl != nil ||
		tenant.KafkaTopicPrefix != nil || len(tenant.GetKafkaBrokers()) != 0 {
		t.Fatalf("private infrastructure leaked to member: %+v", tenant)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestTenantReadRejectsForeignTenantWithoutExistenceOracle(t *testing.T) {
	_, err := (&QuartermasterServer{}).GetTenant(
		tenantCtx("tenant-a", "owner"), &quartermasterpb.GetTenantRequest{TenantId: "tenant-b"},
	)
	if status.Code(err) != codes.PermissionDenied || status.Convert(err).Message() != "tenant access denied" {
		t.Fatalf("foreign tenant denial = %v", err)
	}
}
