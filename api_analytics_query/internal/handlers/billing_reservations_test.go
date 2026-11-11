package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"frameworks/api_analytics_query/internal/database/meteringdb"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/models"
)

type reservationTestProducer struct {
	reports    []models.UsageSummary
	failTenant string
}

func (p *reservationTestProducer) ProduceMessage(_ string, _ []byte, value []byte, _ map[string]string) error {
	var report models.UsageSummary
	if err := json.Unmarshal(value, &report); err != nil {
		return err
	}
	if report.TenantID == p.failTenant {
		return errors.New("injected tenant transport failure")
	}
	p.reports = append(p.reports, report)
	return nil
}

func TestReservationMalformedTenantDoesNotBlockHealthyTenantOrRelease(t *testing.T) {
	for _, failHealthy := range []bool{false, true} {
		t.Run(map[bool]string{false: "healthy publication", true: "independent transport failure"}[failHealthy], func(t *testing.T) {
			ch, chMock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer ch.Close()
			pg, pgMock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer pg.Close()
			chMock.ExpectQuery("WITH viewer_current").WillReturnRows(sqlmock.NewRows([]string{"tenant", "cluster", "minutes", "egress", "unresolved_age"}).
				AddRow("a-bad", "", float64(1), float64(0), float64(3600)).AddRow("b-good", "cell-b", float64(2), float64(0), float64(0)))
			pgMock.ExpectQuery("ListReservationKeys").WithArgs("reservation-test").WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "cluster_id"}).AddRow("a-bad", "old-a").AddRow("c-release", "old-c"))
			if !failHealthy {
				pgMock.ExpectExec("UpsertReservationKey").WithArgs("reservation-test", "b-good", "cell-b", sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
			}
			pgMock.ExpectExec("DeleteReservationKey").WithArgs("reservation-test", "c-release", "old-c").WillReturnResult(sqlmock.NewResult(0, 1))
			producer := &reservationTestProducer{}
			if failHealthy {
				producer.failTenant = "b-good"
			}
			bs := &BillingSummarizer{postgresQueries: meteringdb.New(pg), clickhouse: ch, usageProducer: producer, logger: logging.NewLogger(), sourceID: "reservation-test"}
			if err := bs.PublishUsageReservations(context.Background()); err == nil {
				t.Fatal("unresolved tenant must remain explicitly degraded")
			}
			foundRelease, foundHealthy := false, false
			for _, report := range producer.reports {
				if report.TenantID == "a-bad" {
					t.Fatal("unresolved tenant received fabricated complete/release report")
				}
				if report.TenantID == "b-good" {
					foundHealthy = true
				}
				if report.TenantID == "c-release" {
					foundRelease = true
					if len(report.Meters) != 0 {
						t.Fatal("release contained nonzero usage")
					}
				}
			}
			if !foundRelease || foundHealthy == failHealthy {
				t.Fatalf("independent progress lost: release=%v healthy=%v failHealthy=%v", foundRelease, foundHealthy, failHealthy)
			}
			if err := chMock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
			if err := pgMock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReservationQueryFailureDoesNotReleasePreviousHolds(t *testing.T) {
	ch, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()
	mock.ExpectQuery("WITH viewer_current").WillReturnError(errors.New("source snapshot unavailable"))
	bs := &BillingSummarizer{clickhouse: ch}
	if err := bs.PublishUsageReservations(context.Background()); err == nil {
		t.Fatal("failed snapshot reported success")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
