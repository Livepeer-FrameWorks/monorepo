package cmd

import (
	"context"
	"strings"
	"testing"

	"frameworks/cli/pkg/provisioner"
)

type fakeSkipperSQLExecutor struct {
	query string
	args  []any
	rows  skipperKnowledgeResetResult
}

func (f *fakeSkipperSQLExecutor) Exec(context.Context, provisioner.ConnParams, string) error {
	return nil
}

func (f *fakeSkipperSQLExecutor) QueryRow(context.Context, provisioner.ConnParams, string, []any, ...any) error {
	return nil
}

func (f *fakeSkipperSQLExecutor) QueryRows(_ context.Context, _ provisioner.ConnParams, query string, args []any, scanFn func(scan func(dest ...any) error) error) error {
	f.query = query
	f.args = args
	return scanFn(func(dest ...any) error {
		*dest[0].(*int64) = f.rows.KnowledgeRows
		*dest[1].(*int64) = f.rows.CacheRows
		*dest[2].(*int64) = f.rows.CrawlJobs
		return nil
	})
}

func (f *fakeSkipperSQLExecutor) ExecTx(context.Context, provisioner.ConnParams, func(provisioner.TxExecutor) error) error {
	return nil
}

func TestResetSkipperKnowledgeScopesByTenantAndSource(t *testing.T) {
	exec := &fakeSkipperSQLExecutor{rows: skipperKnowledgeResetResult{KnowledgeRows: 3, CacheRows: 2, CrawlJobs: 1}}
	result, err := resetSkipperKnowledge(context.Background(), exec, provisioner.ConnParams{}, skipperKnowledgeResetOptions{
		TenantID: "tenant-1",
		Source:   "https://docs.example.com/sitemap.xml",
	})
	if err != nil {
		t.Fatalf("resetSkipperKnowledge: %v", err)
	}
	if result.KnowledgeRows != 3 || result.CacheRows != 2 || result.CrawlJobs != 1 {
		t.Fatalf("result = %+v", result)
	}
	if got := exec.args; len(got) != 2 || got[0] != "tenant-1" || got[1] != "https://docs.example.com/sitemap.xml" {
		t.Fatalf("args = %#v", got)
	}
	if !strings.Contains(exec.query, "DELETE FROM skipper.skipper_knowledge") {
		t.Fatalf("reset query should delete knowledge rows: %s", exec.query)
	}
	if !strings.Contains(exec.query, "DELETE FROM skipper.skipper_page_cache") {
		t.Fatalf("reset query should delete page cache rows: %s", exec.query)
	}
	if !strings.Contains(exec.query, "UPDATE skipper.skipper_crawl_jobs") {
		t.Fatalf("reset query should cancel running crawl jobs: %s", exec.query)
	}
}

func TestResetSkipperKnowledgeRequiresTenantUnlessAllTenants(t *testing.T) {
	_, err := resetSkipperKnowledge(context.Background(), &fakeSkipperSQLExecutor{}, provisioner.ConnParams{}, skipperKnowledgeResetOptions{})
	if err == nil {
		t.Fatal("expected tenant requirement error")
	}
}

func TestSkipperKnowledgeResetDryRunDoesNotDelete(t *testing.T) {
	query := skipperKnowledgeResetQuery(true)
	if strings.Contains(query, "DELETE FROM") || strings.Contains(query, "UPDATE skipper.skipper_crawl_jobs") {
		t.Fatalf("dry-run query should be read-only: %s", query)
	}
	if !strings.Contains(query, "COUNT(*) FROM skipper.skipper_knowledge") {
		t.Fatalf("dry-run query should count knowledge rows: %s", query)
	}
}
