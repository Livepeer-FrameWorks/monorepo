package grpc

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestNetworkWorkAddsExactAmountsAcrossRateGroups(t *testing.T) {
	server, mock := newTenantActivityServer(t)
	columns := workTestColumns()
	rows := sqlmock.NewRows(columns).
		AddRow("0xabc", "192.0.2.1", "eu", "720p", "3", 1, 0, 1, 2000, 1, 9007199254740993, 0, 0, 0, 0, 1, "1", 1, 2, 1, 0, 0).
		AddRow("0xabc", "192.0.2.1", "eu", "720p", "6", 1, 0, 1, 2000, 1, 200, 1, 2000, 200, 4000, 1, "4", 1, 1, 1, 1, 1)
	mock.ExpectQuery("FROM periscope.network_observations FINAL WHERE tenant_id=.*kind IN").WillReturnRows(rows)
	mock.ExpectQuery("ticket_credit_numerator!='0'").WillReturnRows(sqlmock.NewRows(creditTestColumns()).
		AddRow("0xabc", "192.0.2.1", "eu", "720p", "2", "7", 1).
		AddRow("0xabc", "192.0.2.1", "eu", "720p", "3", "5", 1))
	now := time.Now().UTC()
	history, err := server.networkWork(context.Background(), "tenant_id=? AND timestamp>=? AND timestamp<?", []any{"scope", now.Add(-time.Hour), now}, now.Add(-time.Hour), now, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Summaries) != 1 {
		t.Fatalf("lost rate groups: %+v", history)
	}
	total := history.Summaries[0]
	if total.DispatchedSourceMS != 4000 || total.AcceptedSourceMS != 2000 || total.ReturnedPixels != 9007199254741193 || total.ServiceCharge.Numerator != "1" || total.ServiceCharge.Denominator != "1" || total.TicketCreditSent.Numerator != "31" || total.TicketCreditSent.Denominator != "6" {
		t.Fatalf("exact work/charge/credit aggregation failed: %+v", total)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestNetworkWorkRetainedEndpointScopeHasNoDecisionPredicate(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherFunc(func(_, actual string) error {
		if strings.Contains(actual, "kind") {
			return fmt.Errorf("work snapshot has no event-kind dimension: %s", actual)
		}
		if !strings.Contains(actual, "resolved_ip=?") {
			return fmt.Errorf("lost endpoint scope")
		}
		return nil
	})))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &PeriscopeServer{clickhouse: db}
	mock.ExpectQuery("work scope").WillReturnRows(sqlmock.NewRows([]string{"empty"}))
	start := time.Date(2026, 8, 1, 12, 13, 0, 0, time.UTC)
	end := time.Date(2026, 10, 8, 12, 47, 0, 0, time.UTC)
	where := "tenant_id=? AND provider=? AND network_id=? AND environment=? AND timestamp>=? AND timestamp<? AND (resolved_ip=? OR (kind='decision' AND resolved_ip=''))"
	args := []any{"scope", "livepeer", "42161", "staging", start, end, "192.0.2.1"}
	history, err := server.networkWork(context.Background(), where, args, start, end, true)
	if err != nil {
		t.Fatal(err)
	}
	if history.Start.Hour() != 13 || history.Start.Minute() != 0 || history.End.Hour() != 12 || history.End.Minute() != 0 {
		t.Fatalf("inaccurate retained accounting bounds: %+v", history)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestNetworkWorkCountsRepeatedExactTicketCredits(t *testing.T) {
	server, mock := newTenantActivityServer(t)
	columns := workTestColumns()
	numerator := "115792089237316195423570985008687907853269984665640564039457584007913129639934000000000000000000"
	denominator := "115792089237316195423570985008687907853269984665640564039457584007913129639935"
	rows := sqlmock.NewRows(columns).AddRow("0xabc", "192.0.2.1", "eu", "720p", "1", 3, 0, 3, 6000, 0, 0, 0, 0, 0, 0, 0, "0", 3, 3, 3, 0, 0)
	mock.ExpectQuery("FROM periscope.network_observations FINAL").WillReturnRows(rows)
	mock.ExpectQuery("ticket_credit_numerator!='0'").WillReturnRows(sqlmock.NewRows(creditTestColumns()).AddRow("0xabc", "192.0.2.1", "eu", "720p", denominator, numerator, 3))
	now := time.Now().UTC()
	history, err := server.networkWork(context.Background(), "tenant_id=?", []any{"scope"}, now.Add(-time.Hour), now, false)
	if err != nil {
		t.Fatal(err)
	}
	total := history.Summaries[0]
	expected, ok := new(big.Rat).SetString(numerator + "/" + denominator)
	if !ok {
		t.Fatal("invalid test ticket fraction")
	}
	expected.Mul(expected, big.NewRat(3, 1))
	if total.TicketCreditSent.Numerator != expected.Num().String() || total.TicketCreditSent.Denominator != expected.Denom().String() || total.ServiceCharge != nil {
		t.Fatalf("repeated exact ticket credits lost: %+v", total)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func workTestColumns() []string {
	return []string{"resource", "ip", "region", "profile", "charge_denominator", "attempts", "unknown", "dispatched", "source", "returned", "pixels", "accepted", "accepted_source", "accepted_pixels", "rendition_ms", "charge_samples", "charge_num", "credit_samples", "tickets", "dispatch_duration_samples", "accepted_duration_samples", "accepted_output_samples"}
}

func creditTestColumns() []string {
	return []string{"resource", "ip", "region", "profile", "denominator", "numerator", "samples"}
}

func TestNetworkWorkDistinguishesZeroCreditFromMissingEvidence(t *testing.T) {
	for _, known := range []uint64{0, 1} {
		t.Run(fmt.Sprint(known), func(t *testing.T) {
			server, mock := newTenantActivityServer(t)
			mock.ExpectQuery("FROM periscope.network_observations FINAL").WillReturnRows(sqlmock.NewRows(workTestColumns()).
				AddRow("0xabc", "192.0.2.1", "eu", "720p", "1", 1, 0, 1, 2000, 0, 0, 0, 0, 0, 0, 0, "0", known, 0, 1, 0, 0))
			mock.ExpectQuery("ticket_credit_numerator!='0'").WillReturnRows(sqlmock.NewRows(creditTestColumns()))
			now := time.Now().UTC()
			history, err := server.networkWork(context.Background(), "tenant_id=?", []any{"scope"}, now.Add(-time.Hour), now, false)
			if err != nil {
				t.Fatal(err)
			}
			credit := history.Summaries[0].TicketCreditSent
			if known == 0 && credit != nil || known == 1 && (credit == nil || credit.Numerator != "0" || credit.Denominator != "1") {
				t.Fatalf("missing and known-zero credit confused: %+v", credit)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
