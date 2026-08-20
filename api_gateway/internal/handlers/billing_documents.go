package handlers

import (
	"context"
	"net/http"
	"regexp"
	"strings"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/gin-gonic/gin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type billingDocumentClient interface {
	ListBillingDocuments(ctx context.Context, tenantID string) (*purserpb.ListBillingDocumentsResponse, error)
	GetBillingDocument(ctx context.Context, tenantID, kind, documentID string) (*purserpb.GetBillingDocumentResponse, error)
}

type BillingDocumentHandlers struct {
	purser billingDocumentClient
}

func NewBillingDocumentHandlers(purser billingDocumentClient) *BillingDocumentHandlers {
	return &BillingDocumentHandlers{purser: purser}
}

func billingDocumentHTTPStatus(err error) int {
	switch status.Code(err) {
	case codes.InvalidArgument:
		return http.StatusBadRequest
	case codes.PermissionDenied, codes.Unauthenticated:
		return http.StatusForbidden
	case codes.NotFound:
		return http.StatusNotFound
	case codes.FailedPrecondition:
		return http.StatusConflict
	case codes.Unavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

func billingDocumentTenant(c *gin.Context) (string, bool) {
	tenantID := ctxkeys.GetTenantID(c.Request.Context())
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "tenant context required"})
		return "", false
	}
	return tenantID, true
}

func (h *BillingDocumentHandlers) List() gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID, ok := billingDocumentTenant(c)
		if !ok {
			return
		}
		response, err := h.purser.ListBillingDocuments(c.Request.Context(), tenantID)
		if err != nil {
			c.JSON(billingDocumentHTTPStatus(err), gin.H{"error": status.Convert(err).Message()})
			return
		}
		c.Header("Cache-Control", "private, no-store")
		c.JSON(http.StatusOK, response)
	}
}

var safeBillingDocumentFilename = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func (h *BillingDocumentHandlers) Download() gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID, ok := billingDocumentTenant(c)
		if !ok {
			return
		}
		response, err := h.purser.GetBillingDocument(c.Request.Context(), tenantID, c.Param("kind"), c.Param("id"))
		if err != nil {
			c.JSON(billingDocumentHTTPStatus(err), gin.H{"error": status.Convert(err).Message()})
			return
		}
		filename := "billing-document.html"
		if response.GetDocument() != nil && response.GetDocument().GetDownloadFilename() != "" {
			filename = safeBillingDocumentFilename.ReplaceAllString(response.GetDocument().GetDownloadFilename(), "-")
			filename = strings.Trim(filename, ".-")
			if filename == "" {
				filename = "billing-document.html"
			}
		}
		c.Header("Cache-Control", "private, no-store")
		c.Header("Content-Disposition", `attachment; filename="`+filename+`"`)
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Document-SHA256", response.GetSha256())
		contentType := response.GetContentType()
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		c.Data(http.StatusOK, contentType, response.GetContent())
	}
}
