package bootstrap

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// expectS3Probe rows a committed descriptor on the probe. prefixSet mirrors "s3_prefix IS NOT NULL": true when the
// prefix is part of an established (adopted) descriptor, false for a pre-migration row whose prefix column is still
// NULL (established without a known prefix — not yet adopted).
func expectS3Probe(mock sqlmock.Sqlmock, bucket, endpoint, region string, prefixSet bool, prefix string) {
	mock.ExpectQuery(regexp.QuoteMeta("FROM quartermaster.infrastructure_clusters")).
		WithArgs("c1").
		WillReturnRows(sqlmock.NewRows([]string{
			"cluster_name", "cluster_type", "owner_tenant_id", "base_url", "wg_mesh_cidr", "wg_listen_port",
			"is_default_cluster", "is_platform_official", "public_topology", "allow_private_pull_sources",
			"region_id", "cell_id", "cluster_class", "control_cell_id", "eligible_serving_cell_ids",
			"s3_bucket", "s3_endpoint", "s3_region", "s3_prefix_set", "s3_prefix",
		}).AddRow(
			"eu", "regional", "", "", "", 0,
			false, false, false, false,
			"", "", "", "", "{}",
			bucket, endpoint, region, prefixSet, prefix,
		))
}

// A cluster's S3 backend descriptor is IMMUTABLE once established (bucket set): repointing, clearing, or filling a
// previously-empty tuple field would misroute cleanup/serving of historical bytes (Chandler reads this row; Foghorn
// enforces the same locally). upsertCluster must refuse all three.
func TestUpsertClusterFreezesS3Descriptor(t *testing.T) {
	cases := []struct {
		name    string
		desired Cluster
	}{
		{"repoint bucket", Cluster{ID: "c1", Name: "eu", Type: "regional", S3Bucket: "bucket-B", S3Endpoint: "https://a.s3", S3Region: "us-east-1"}},
		{"repoint endpoint", Cluster{ID: "c1", Name: "eu", Type: "regional", S3Bucket: "bucket-A", S3Endpoint: "https://b.s3", S3Region: "us-east-1"}},
		{"clear the descriptor", Cluster{ID: "c1", Name: "eu", Type: "regional", S3Bucket: "", S3Endpoint: "", S3Region: ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			defer db.Close()                                                       //nolint:errcheck
			expectS3Probe(mock, "bucket-A", "https://a.s3", "us-east-1", true, "") // established descriptor (adopted, empty prefix)
			// No UPDATE may be issued — the drift guard fires first.
			if _, err := upsertCluster(context.Background(), db, tc.desired, ""); err == nil || !strings.Contains(err.Error(), "s3 descriptor drift") {
				t.Fatalf("must refuse with s3 descriptor drift, got: %v", err)
			}
			if mErr := mock.ExpectationsWereMet(); mErr != nil {
				t.Fatalf("expectations: %v", mErr)
			}
		})
	}

	// Filling a previously-empty field (endpoint) when bucket is already set is also a repoint of the effective tuple.
	t.Run("fill previously-empty endpoint", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer db.Close()                                           //nolint:errcheck
		expectS3Probe(mock, "bucket-A", "", "us-east-1", true, "") // endpoint was empty at establishment
		c := Cluster{ID: "c1", Name: "eu", Type: "regional", S3Bucket: "bucket-A", S3Endpoint: "https://new.s3", S3Region: "us-east-1"}
		if _, err := upsertCluster(context.Background(), db, c, ""); err == nil || !strings.Contains(err.Error(), "s3 descriptor drift") {
			t.Fatalf("filling a previously-empty field must be refused, got: %v", err)
		}
		if mErr := mock.ExpectationsWereMet(); mErr != nil {
			t.Fatalf("expectations: %v", mErr)
		}
	})
}

// A pre-migration cell (bucket/endpoint/region established, s3_prefix still NULL) must be able to ADOPT its prefix
// exactly once — the migration transition. After adoption the prefix is frozen like the rest of the tuple.
func TestUpsertClusterAdoptsPrefixOnce(t *testing.T) {
	// Establish a non-empty prefix from an incomplete NULL state → allowed, issues an UPDATE.
	t.Run("adopt non-empty prefix from NULL", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer db.Close() //nolint:errcheck
		expectS3Probe(mock, "bucket-A", "https://a.s3", "us-east-1", false, "")
		mock.ExpectExec(regexp.QuoteMeta("UPDATE quartermaster.infrastructure_clusters")).
			WillReturnResult(sqlmock.NewResult(0, 1))
		c := Cluster{ID: "c1", Name: "eu", Type: "regional", S3Bucket: "bucket-A", S3Endpoint: "https://a.s3", S3Region: "us-east-1", S3Prefix: "cell-a"}
		if status, err := upsertCluster(context.Background(), db, c, ""); err != nil || status != "updated" {
			t.Fatalf("adopting a prefix from NULL must succeed with an update, got status=%q err=%v", status, err)
		}
		if mErr := mock.ExpectationsWereMet(); mErr != nil {
			t.Fatalf("expectations: %v", mErr)
		}
	})

	// Adopt a known-empty prefix from NULL → NOT a noop; must UPDATE so the row persists an explicit '' (adopted).
	t.Run("adopt known-empty prefix from NULL is not a noop", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer db.Close() //nolint:errcheck
		expectS3Probe(mock, "bucket-A", "https://a.s3", "us-east-1", false, "")
		mock.ExpectExec(regexp.QuoteMeta("UPDATE quartermaster.infrastructure_clusters")).
			WillReturnResult(sqlmock.NewResult(0, 1))
		c := Cluster{ID: "c1", Name: "eu", Type: "regional", S3Bucket: "bucket-A", S3Endpoint: "https://a.s3", S3Region: "us-east-1", S3Prefix: ""}
		if status, err := upsertCluster(context.Background(), db, c, ""); err != nil || status != "updated" {
			t.Fatalf("adopting an empty prefix from NULL must UPDATE (not noop), got status=%q err=%v", status, err)
		}
		if mErr := mock.ExpectationsWereMet(); mErr != nil {
			t.Fatalf("expectations: %v", mErr)
		}
	})

	// Once adopted (prefix set), changing it is a refused repoint.
	t.Run("repoint adopted prefix refused", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer db.Close() //nolint:errcheck
		expectS3Probe(mock, "bucket-A", "https://a.s3", "us-east-1", true, "cell-a")
		c := Cluster{ID: "c1", Name: "eu", Type: "regional", S3Bucket: "bucket-A", S3Endpoint: "https://a.s3", S3Region: "us-east-1", S3Prefix: "cell-b"}
		if _, err := upsertCluster(context.Background(), db, c, ""); err == nil || !strings.Contains(err.Error(), "s3 prefix drift") {
			t.Fatalf("repointing an adopted prefix must be refused, got: %v", err)
		}
		if mErr := mock.ExpectationsWereMet(); mErr != nil {
			t.Fatalf("expectations: %v", mErr)
		}
	})

	// An already-adopted matching prefix is a noop (no UPDATE).
	t.Run("adopted matching prefix is noop", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer db.Close() //nolint:errcheck
		expectS3Probe(mock, "bucket-A", "https://a.s3", "us-east-1", true, "cell-a")
		c := Cluster{ID: "c1", Name: "eu", Type: "regional", S3Bucket: "bucket-A", S3Endpoint: "https://a.s3", S3Region: "us-east-1", S3Prefix: "cell-a"}
		if status, err := upsertCluster(context.Background(), db, c, ""); err != nil || status != "noop" {
			t.Fatalf("an adopted matching descriptor must be a noop, got status=%q err=%v", status, err)
		}
		if mErr := mock.ExpectationsWereMet(); mErr != nil {
			t.Fatalf("expectations: %v", mErr)
		}
	})
}
