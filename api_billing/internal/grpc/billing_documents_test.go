package grpc

import (
	"strings"
	"testing"
	"time"
)

func TestRenderBillingDocumentEscapesPersistedCustomerData(t *testing.T) {
	now := time.Now().UTC()
	response, err := renderBillingDocument(billingDocumentRow{
		id: "id", kind: "invoice", number: "INV-1", amountCents: 1234,
		currency: "EUR", status: "paid", issuedAt: now, retentionUntil: now.AddDate(10, 0, 0),
	}, billingDocumentHTMLData{
		Title: "Invoice", SupplierName: "FrameWorks", Customer: `<script>alert("x")</script>`,
	})
	if err != nil {
		t.Fatal(err)
	}
	content := string(response.GetContent())
	if strings.Contains(content, "<script>") || !strings.Contains(content, "&lt;script&gt;") {
		t.Fatalf("customer field was not safely escaped: %s", content)
	}
	if response.GetSha256() == "" || response.GetDocument().GetDownloadFilename() != "INV-1.html" {
		t.Fatalf("missing document integrity metadata: %+v", response)
	}
}
