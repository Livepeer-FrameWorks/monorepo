package chat

import (
	"context"
	"errors"
	"testing"
	"time"

	"frameworks/pkg/ctxkeys"
	pb "frameworks/pkg/proto"
)

type mockReportQuerier struct {
	reports    []ReportData
	total      int
	unread     int
	markedRead int
	getReport  ReportData
	err        error
}

func (m *mockReportQuerier) ListPaginated(_ context.Context, _ string, _, _ int) ([]ReportData, int, error) {
	return m.reports, m.total, m.err
}

func (m *mockReportQuerier) GetByID(_ context.Context, _, _ string) (ReportData, error) {
	return m.getReport, m.err
}

func (m *mockReportQuerier) MarkRead(_ context.Context, _ string, _ []string) (int, error) {
	return m.markedRead, m.err
}

func (m *mockReportQuerier) UnreadCount(_ context.Context, _ string) (int, error) {
	return m.unread, m.err
}

func tenantContext(tenantID string) context.Context {
	ctx := context.Background()
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, tenantID)
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, "user-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyAuthType, "jwt")
	return ctx
}

func TestListReportsReturnsReports(t *testing.T) {
	now := time.Now().UTC()
	mock := &mockReportQuerier{
		reports: []ReportData{
			{ID: "r-1", Summary: "Test report", CreatedAt: now},
		},
		total:  1,
		unread: 1,
	}
	srv := NewGRPCServer(GRPCServerConfig{Reports: mock})

	ctx := tenantContext("tenant-a")
	resp, err := srv.ListReports(ctx, &pb.ListSkipperReportsRequest{Limit: 10})
	if err != nil {
		t.Fatalf("ListReports: %v", err)
	}
	if len(resp.Reports) != 1 {
		t.Fatalf("expected 1 report, got %d", len(resp.Reports))
	}
	if resp.Reports[0].Id != "r-1" {
		t.Fatalf("expected r-1, got %s", resp.Reports[0].Id)
	}
	if resp.TotalCount != 1 {
		t.Fatalf("expected total=1, got %d", resp.TotalCount)
	}
	if resp.UnreadCount != 1 {
		t.Fatalf("expected unread=1, got %d", resp.UnreadCount)
	}
}

func TestListReportsRequiresTenant(t *testing.T) {
	srv := NewGRPCServer(GRPCServerConfig{Reports: &mockReportQuerier{}})
	_, err := srv.ListReports(context.Background(), &pb.ListSkipperReportsRequest{})
	if err == nil {
		t.Fatal("expected error for missing tenant")
	}
}

func TestGetReportReturnsReport(t *testing.T) {
	now := time.Now().UTC()
	mock := &mockReportQuerier{
		getReport: ReportData{
			ID:              "r-1",
			Summary:         "Test",
			MetricsReviewed: []string{"fps"},
			Recommendations: []ReportRecommendation{{Text: "fix it", Confidence: "high"}},
			CreatedAt:       now,
		},
	}
	srv := NewGRPCServer(GRPCServerConfig{Reports: mock})

	ctx := tenantContext("tenant-a")
	resp, err := srv.GetReport(ctx, &pb.GetSkipperReportRequest{Id: "r-1"})
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if resp.Id != "r-1" {
		t.Fatalf("expected r-1, got %s", resp.Id)
	}
	if len(resp.Recommendations) != 1 {
		t.Fatalf("expected 1 recommendation, got %d", len(resp.Recommendations))
	}
}

func TestMarkReportsReadReturnsCount(t *testing.T) {
	mock := &mockReportQuerier{markedRead: 3}
	srv := NewGRPCServer(GRPCServerConfig{Reports: mock})

	ctx := tenantContext("tenant-a")
	resp, err := srv.MarkReportsRead(ctx, &pb.MarkSkipperReportsReadRequest{Ids: []string{"r-1", "r-2", "r-3"}})
	if err != nil {
		t.Fatalf("MarkReportsRead: %v", err)
	}
	if resp.MarkedCount != 3 {
		t.Fatalf("expected 3, got %d", resp.MarkedCount)
	}
}

func TestGetUnreadReportCountReturnsCount(t *testing.T) {
	mock := &mockReportQuerier{unread: 5}
	srv := NewGRPCServer(GRPCServerConfig{Reports: mock})

	ctx := tenantContext("tenant-a")
	resp, err := srv.GetUnreadReportCount(ctx, &pb.GetUnreadReportCountRequest{})
	if err != nil {
		t.Fatalf("GetUnreadReportCount: %v", err)
	}
	if resp.Count != 5 {
		t.Fatalf("expected 5, got %d", resp.Count)
	}
}

func TestReportMethodsHandleStoreErrors(t *testing.T) {
	mock := &mockReportQuerier{err: errors.New("db down")}
	srv := NewGRPCServer(GRPCServerConfig{Reports: mock})
	ctx := tenantContext("tenant-a")

	if _, err := srv.ListReports(ctx, &pb.ListSkipperReportsRequest{}); err == nil {
		t.Fatal("expected error from ListReports")
	}
	if _, err := srv.GetReport(ctx, &pb.GetSkipperReportRequest{Id: "r-1"}); err == nil {
		t.Fatal("expected error from GetReport")
	}
	if _, err := srv.MarkReportsRead(ctx, &pb.MarkSkipperReportsReadRequest{}); err == nil {
		t.Fatal("expected error from MarkReportsRead")
	}
	if _, err := srv.GetUnreadReportCount(ctx, &pb.GetUnreadReportCountRequest{}); err == nil {
		t.Fatal("expected error from GetUnreadReportCount")
	}
}

func TestReportReadAtSerializedWhenSet(t *testing.T) {
	now := time.Now().UTC()
	mock := &mockReportQuerier{
		getReport: ReportData{
			ID:        "r-1",
			CreatedAt: now,
			ReadAt:    &now,
		},
	}
	srv := NewGRPCServer(GRPCServerConfig{Reports: mock})
	ctx := tenantContext("tenant-a")

	resp, err := srv.GetReport(ctx, &pb.GetSkipperReportRequest{Id: "r-1"})
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if resp.ReadAt == nil {
		t.Fatal("expected ReadAt to be set")
	}
}
