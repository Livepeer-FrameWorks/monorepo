package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/gin-gonic/gin"
)

type fakeBillingDocumentClient struct {
	listTenant string
	getTenant  string
	getKind    string
	getID      string
}

func (f *fakeBillingDocumentClient) ListBillingDocuments(_ context.Context, tenantID string) (*purserpb.ListBillingDocumentsResponse, error) {
	f.listTenant = tenantID
	return &purserpb.ListBillingDocumentsResponse{Documents: []*purserpb.BillingDocument{{Id: "doc", Kind: "invoice"}}}, nil
}

func (f *fakeBillingDocumentClient) GetBillingDocument(_ context.Context, tenantID, kind, documentID string) (*purserpb.GetBillingDocumentResponse, error) {
	f.getTenant, f.getKind, f.getID = tenantID, kind, documentID
	return &purserpb.GetBillingDocumentResponse{
		Document:    &purserpb.BillingDocument{DownloadFilename: "INV-0001.html"},
		ContentType: "text/html; charset=utf-8", Content: []byte("<html>invoice</html>"), Sha256: "abc123",
	}, nil
}

func TestBillingDocumentDownloadIsTenantBoundAttachment(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := &fakeBillingDocumentClient{}
	handler := NewBillingDocumentHandlers(fake)
	router := gin.New()
	router.GET("/v1/billing/documents/:kind/:id", func(c *gin.Context) {
		ctx := context.WithValue(c.Request.Context(), ctxkeys.KeyTenantID, "11111111-1111-1111-1111-111111111111")
		c.Request = c.Request.WithContext(ctx)
		handler.Download()(c)
	})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v1/billing/documents/invoice/22222222-2222-2222-2222-222222222222", nil)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if fake.getTenant != "11111111-1111-1111-1111-111111111111" || fake.getKind != "invoice" || fake.getID != "22222222-2222-2222-2222-222222222222" {
		t.Fatalf("unexpected downstream request: %+v", fake)
	}
	if disposition := recorder.Header().Get("Content-Disposition"); disposition != `attachment; filename="INV-0001.html"` {
		t.Fatalf("content disposition = %q", disposition)
	}
	if recorder.Header().Get("Cache-Control") != "private, no-store" || recorder.Header().Get("X-Document-SHA256") != "abc123" {
		t.Fatalf("missing immutable/private headers: %v", recorder.Header())
	}
}

func TestBillingDocumentDownloadRequiresTenantContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/:kind/:id", NewBillingDocumentHandlers(&fakeBillingDocumentClient{}).Download())
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/invoice/id", nil))
	if recorder.Code != http.StatusUnauthorized || !strings.Contains(recorder.Body.String(), "tenant context required") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
