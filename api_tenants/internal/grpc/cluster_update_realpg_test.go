//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"slices"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/lib/pq"
	"google.golang.org/protobuf/proto"
)

func TestUpdateClusterFields_RealPG(t *testing.T) {
	verifyUpdateClusterFields(t, openQuartermasterRealPG(t, "cluster-update"))
}

func TestUpdateClusterFields_RealYugabyte(t *testing.T) {
	db, ok := dockerpg.OpenSharedYugabyteBaseline(t, "quartermaster_cluster_update", "quartermaster")
	if !ok {
		t.Skip("requires shared Yugabyte contract fixture")
	}
	verifyUpdateClusterFields(t, db)
}

func verifyUpdateClusterFields(t *testing.T, db *sql.DB) {
	t.Helper()
	const cluster = "cluster-update"
	if _, err := db.Exec(`INSERT INTO quartermaster.infrastructure_clusters
		(cluster_id, cluster_name, cluster_type, base_url)
		VALUES ($1, 'Update contract', 'edge', 'https://edge.example')`, cluster); err != nil {
		t.Fatal(err)
	}
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	hasher, err := events.NewTokenHasher("cluster-update-test-secret")
	if err != nil {
		t.Fatal(err)
	}
	server.SetEventTokenHasher(hasher)
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")
	tests := []struct {
		name string
		req  *quartermasterpb.UpdateClusterRequest
	}{
		{"enable private pulls", &quartermasterpb.UpdateClusterRequest{AllowPrivatePullSources: proto.Bool(true)}},
		{"disable private pulls", &quartermasterpb.UpdateClusterRequest{AllowPrivatePullSources: proto.Bool(false)}},
		{"database", &quartermasterpb.UpdateClusterRequest{DatabaseUrl: proto.String("postgres://db.example/media")}},
		{"periscope", &quartermasterpb.UpdateClusterRequest{PeriscopeUrl: proto.String("https://periscope.example")}},
		{"kafka", &quartermasterpb.UpdateClusterRequest{KafkaBrokers: []string{"kafka-a:9092", "kafka-b:9092"}}},
		{"streams", &quartermasterpb.UpdateClusterRequest{MaxConcurrentStreams: proto.Int32(23)}},
		{"viewers", &quartermasterpb.UpdateClusterRequest{MaxConcurrentViewers: proto.Int32(456)}},
		{"clear streams", &quartermasterpb.UpdateClusterRequest{MaxConcurrentStreams: proto.Int32(0)}},
		{"clear viewers", &quartermasterpb.UpdateClusterRequest{MaxConcurrentViewers: proto.Int32(0)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.req.ClusterId = cluster
			resp, err := server.UpdateCluster(ctx, test.req)
			if err != nil {
				t.Fatalf("UpdateCluster field %s: %v", test.name, err)
			}
			got := resp.GetCluster()
			if test.req.AllowPrivatePullSources != nil {
				var stored bool
				if err := db.QueryRowContext(ctx, `SELECT allow_private_pull_sources FROM quartermaster.infrastructure_clusters WHERE cluster_id=$1`, cluster).Scan(&stored); err != nil || stored != test.req.GetAllowPrivatePullSources() {
					t.Fatalf("private-source write: stored=%v err=%v", stored, err)
				}
				if got.GetAllowPrivatePullSources() != test.req.GetAllowPrivatePullSources() {
					t.Fatalf("private-source readback=%v want %v", got.GetAllowPrivatePullSources(), test.req.GetAllowPrivatePullSources())
				}
				read, err := server.GetCluster(ctx, &quartermasterpb.GetClusterRequest{ClusterId: cluster})
				if err != nil || read.GetCluster().GetAllowPrivatePullSources() != stored {
					t.Fatalf("GetCluster lost stored private-source permission: response=%v err=%v", read, err)
				}
			}
			if test.req.DatabaseUrl != nil && got.GetDatabaseUrl() != test.req.GetDatabaseUrl() {
				t.Fatalf("database URL = %q", got.GetDatabaseUrl())
			}
			if test.req.PeriscopeUrl != nil && got.GetPeriscopeUrl() != test.req.GetPeriscopeUrl() {
				t.Fatalf("periscope URL = %q", got.GetPeriscopeUrl())
			}
			if len(test.req.KafkaBrokers) > 0 && !slices.Equal(got.GetKafkaBrokers(), test.req.KafkaBrokers) {
				t.Fatalf("Kafka brokers = %v", got.GetKafkaBrokers())
			}
			if test.req.MaxConcurrentStreams != nil && got.GetMaxConcurrentStreams() != test.req.GetMaxConcurrentStreams() {
				t.Fatalf("stream cap = %d", got.GetMaxConcurrentStreams())
			}
			if test.req.MaxConcurrentViewers != nil && got.GetMaxConcurrentViewers() != test.req.GetMaxConcurrentViewers() {
				t.Fatalf("viewer cap = %d", got.GetMaxConcurrentViewers())
			}
		})
	}
	var databaseURL, periscopeURL string
	var brokers pq.StringArray
	if err := db.QueryRow(`SELECT database_url, periscope_url, kafka_brokers
		FROM quartermaster.infrastructure_clusters WHERE cluster_id = $1`, cluster).
		Scan(&databaseURL, &periscopeURL, &brokers); err != nil {
		t.Fatal(err)
	}
	if databaseURL != "postgres://db.example/media" || periscopeURL != "https://periscope.example" || len(brokers) != 2 {
		t.Fatalf("stored infrastructure fields: %q %q %v", databaseURL, periscopeURL, brokers)
	}
}
