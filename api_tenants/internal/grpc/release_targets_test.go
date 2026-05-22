package grpc

import (
	"context"
	"testing"
	"time"

	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestValidateRolloutPlanJSONRejectsInvalidFieldType(t *testing.T) {
	t.Parallel()

	err := validateRolloutPlanJSON(`{"batch_size":"wide"}`)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("status code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestValidateRolloutPlanJSONRejectsInvalidDrainDeadline(t *testing.T) {
	t.Parallel()

	err := validateRolloutPlanJSON(`{"drain_deadline":"soon"}`)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("status code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestValidateRolloutPlanJSONRejectsUnsupportedCapacityFloor(t *testing.T) {
	t.Parallel()

	err := validateRolloutPlanJSON(`{"capacity_floor":2}`)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("status code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestValidateRolloutPlanJSONAcceptsValidPlan(t *testing.T) {
	t.Parallel()

	if err := validateRolloutPlanJSON(`{"batch_size":2,"drain_deadline":"30m","force":true}`); err != nil {
		t.Fatalf("validateRolloutPlanJSON: %v", err)
	}
}

func TestValidateRolloutPlanJSONRejectsUnknownKey(t *testing.T) {
	t.Parallel()

	err := validateRolloutPlanJSON(`{"batch_size":2,"max_parallel":4}`)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("status code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestValidateRolloutPlanJSONRejectsCamelCaseTypo(t *testing.T) {
	t.Parallel()

	err := validateRolloutPlanJSON(`{"capacityFloor":2}`)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("status code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestNormalizeReleaseTargetChannelRejectsUnknownChannel(t *testing.T) {
	t.Parallel()

	_, err := normalizeReleaseTargetChannel(" nightly ")
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("status code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestNormalizeReleaseTargetChannelRejectsEdgeAsTrack(t *testing.T) {
	t.Parallel()

	_, err := normalizeReleaseTargetChannel("edge")
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("status code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestNormalizeReleaseTargetChannelTrimsAndLowercases(t *testing.T) {
	t.Parallel()

	channel, err := normalizeReleaseTargetChannel(" STABLE ")
	if err != nil {
		t.Fatalf("normalizeReleaseTargetChannel: %v", err)
	}
	if channel != "stable" {
		t.Fatalf("channel = %q, want stable", channel)
	}
}

func TestValidateEdgeReleaseComponentsRejectsUnknownComponent(t *testing.T) {
	t.Parallel()

	err := validateEdgeReleaseComponents(`{"mistake":{"version":"v1.2.3","artifact_url":"https://example.test/a.tgz","checksum":"abc"}}`)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("status code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestValidateEdgeReleaseComponentsRejectsMultilineVersion(t *testing.T) {
	t.Parallel()

	err := validateEdgeReleaseComponents("{\"helmsman\":{\"version\":\"v1.2.3\\nEXTRA=1\",\"artifact_url\":\"https://example.test/helmsman.tgz\",\"checksum\":\"abc\"}}")
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("status code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestValidateEdgeReleaseComponentsRejectsMalformedChecksum(t *testing.T) {
	t.Parallel()

	err := validateEdgeReleaseComponents(`{"helmsman":{"version":"v1.2.3","artifacts":{"linux/amd64":{"artifact_url":"https://example.test/helmsman.tgz","checksum":"abc"}}}}`)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("status code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestValidateEdgeReleaseComponentsRejectsLegacySingleArtifactShape(t *testing.T) {
	t.Parallel()

	err := validateEdgeReleaseComponents(`{"helmsman":{"version":"v1.2.3","artifact_url":"https://example.test/helmsman.tgz","checksum":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("status code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestValidateEdgeReleaseComponentsRejectsInvalidPlatformArtifact(t *testing.T) {
	t.Parallel()

	err := validateEdgeReleaseComponents(`{"helmsman":{"version":"v1.2.3","artifacts":{"linux":{"artifact_url":"https://example.test/helmsman.tgz","checksum":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}}`)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("status code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestValidateEdgeReleaseComponentsRejectsNoUpdateableComponents(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{`{}`, `{"config_schema":{"version":"v1.2.3"}}`} {
		err := validateEdgeReleaseComponents(raw)
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("status code for %s = %v, want InvalidArgument", raw, status.Code(err))
		}
	}
}

func TestValidateEdgeReleaseComponentsAcceptsSupportedComponents(t *testing.T) {
	t.Parallel()

	err := validateEdgeReleaseComponents(`{
		"helmsman":{"version":"v1.2.3","artifacts":{"linux/amd64":{"artifact_url":"https://example.test/helmsman-linux.tgz","checksum":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"darwin/arm64":{"artifact_url":"https://example.test/helmsman-darwin.tgz","checksum":"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"}}},
		"mist":{"version":"v2026.1","artifacts":{"linux-amd64":{"artifact_url":"https://example.test/mist.tgz","checksum":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}},
		"caddy":{"version":"v2.8.4","artifacts":{"linux/amd64":{"artifact_url":"https://example.test/caddy.tgz","checksum":"sha512:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}}},
		"config_schema":{"version":"v1.2.3"}
	}`)
	if err != nil {
		t.Fatalf("validateEdgeReleaseComponents: %v", err)
	}
}

func TestUpsertEdgeReleaseRetriesRetryablePostgresError(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	server := NewQuartermasterServer(db, logrus.New(), nil, nil, nil, nil, nil)
	publishedAt := time.Date(2026, 5, 22, 11, 0, 0, 0, time.UTC)
	components := `{"helmsman":{"version":"v1.2.3","artifacts":{"linux/amd64":{"artifact_url":"https://example.test/helmsman.tgz","checksum":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}}`
	normalizedComponents := `{"helmsman":{"artifacts":{"linux/amd64":{"artifact_url":"https://example.test/helmsman.tgz","checksum":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}},"version":"v1.2.3"}}`

	mock.ExpectQuery(`INSERT INTO quartermaster\.edge_releases`).
		WithArgs("stable", "v1.2.3", normalizedComponents, publishedAt).
		WillReturnError(&pq.Error{Code: "40001", Message: "schema version mismatch for table x: expected 89, got 88"})
	mock.ExpectQuery(`INSERT INTO quartermaster\.edge_releases`).
		WithArgs("stable", "v1.2.3", normalizedComponents, publishedAt).
		WillReturnRows(sqlmock.NewRows([]string{"channel", "version", "components", "published_at"}).
			AddRow("stable", "v1.2.3", normalizedComponents, publishedAt))

	resp, err := server.UpsertEdgeRelease(serviceCtx(), &pb.UpsertEdgeReleaseRequest{Release: &pb.EdgeRelease{
		Channel:        "stable",
		Version:        "v1.2.3",
		ComponentsJson: components,
		PublishedAt:    timestamppb.New(publishedAt),
	}})
	if err != nil {
		t.Fatalf("UpsertEdgeRelease: %v", err)
	}
	if got := resp.GetRelease().GetVersion(); got != "v1.2.3" {
		t.Fatalf("version = %q, want v1.2.3", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestSetClusterReleaseTargetRetriesRetryablePostgresError(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	server := NewQuartermasterServer(db, logrus.New(), nil, nil, nil, nil, nil)
	updatedAt := time.Date(2026, 5, 22, 11, 0, 0, 0, time.UTC)

	mock.ExpectQuery(`SELECT EXISTS \([\s\S]*FROM quartermaster\.edge_releases[\s\S]*WHERE channel = \$1 AND version = \$2`).
		WithArgs("stable", "v1.2.3").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(`INSERT INTO quartermaster\.cluster_release_targets`).
		WithArgs("media-eu-1", "stable", "v1.2.3", "{}", false).
		WillReturnError(&pq.Error{Code: "40001", Message: "schema version mismatch for table x: expected 89, got 88"})
	mock.ExpectQuery(`SELECT EXISTS \([\s\S]*FROM quartermaster\.edge_releases[\s\S]*WHERE channel = \$1 AND version = \$2`).
		WithArgs("stable", "v1.2.3").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(`INSERT INTO quartermaster\.cluster_release_targets`).
		WithArgs("media-eu-1", "stable", "v1.2.3", "{}", false).
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id", "channel", "target_version", "rollout_plan", "paused", "updated_at"}).
			AddRow("media-eu-1", "stable", "v1.2.3", "{}", false, updatedAt))

	resp, err := server.SetClusterReleaseTarget(serviceCtx(), &pb.SetClusterReleaseTargetRequest{Target: &pb.ClusterReleaseTarget{
		ClusterId:       "media-eu-1",
		Channel:         "stable",
		TargetVersion:   "v1.2.3",
		RolloutPlanJson: "{}",
		Paused:          false,
	}})
	if err != nil {
		t.Fatalf("SetClusterReleaseTarget: %v", err)
	}
	if got := resp.GetTarget().GetClusterId(); got != "media-eu-1" {
		t.Fatalf("cluster_id = %q, want media-eu-1", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestEnsureEdgeReleaseTargetExistsRejectsEmptyChannelCatalog(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	server := NewQuartermasterServer(db, logrus.New(), nil, nil, nil, nil, nil)

	mock.ExpectQuery(`SELECT EXISTS \([\s\S]*FROM quartermaster\.edge_releases[\s\S]*WHERE channel = \$1`).
		WithArgs("stable").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	err = server.ensureEdgeReleaseTargetExists(context.Background(), "stable", "")
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("status code = %v, want InvalidArgument", status.Code(err))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestEnsureEdgeReleaseTargetExistsAcceptsPublishedVersion(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	server := NewQuartermasterServer(db, logrus.New(), nil, nil, nil, nil, nil)

	mock.ExpectQuery(`SELECT EXISTS \([\s\S]*FROM quartermaster\.edge_releases[\s\S]*WHERE channel = \$1 AND version = \$2`).
		WithArgs("rc", "v1.2.3-rc1").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

	if err := server.ensureEdgeReleaseTargetExists(context.Background(), "rc", "v1.2.3-rc1"); err != nil {
		t.Fatalf("ensureEdgeReleaseTargetExists: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
