package cmd

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type financialPageFunc func(context.Context, *purserpb.AdminFinancialAuditPageRequest) (*purserpb.AdminFinancialAuditPageResponse, error)

func TestFinancialAuditAcceptsFrozenRegularInvoiceTax(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want string
	}{
		{`{"id":"invoice-1","status":"paid","document_kind":"invoice","tax_status":"ready","tax_policy_version":"exclusive-service-invoice-v1","net_eur_cents":10000,"vat_eur_cents":2100,"gross_eur_cents":12100}`, ""},
		{`{"id":"invoice-1","status":"paid","document_kind":"invoice","tax_status":"ready","tax_policy_version":"exclusive-service-invoice-v1","net_eur_cents":10000,"vat_eur_cents":2100,"gross_eur_cents":10000}`, "TAX_TOTALS_MISMATCH"},
		{`{"id":"historic","status":"paid","document_kind":"invoice","tax_status":"legacy_unknown","net_eur_cents":null}`, "UNKNOWN_TAX_TREATMENT"},
	} {
		var data map[string]json.RawMessage
		if err := json.Unmarshal([]byte(tc.raw), &data); err != nil {
			t.Fatal(err)
		}
		got := auditTaxException("invoices", "tenant", data)
		if tc.want == "" {
			if got != nil {
				t.Fatalf("fully taxed invoice falsely flagged: %+v", got)
			}
		} else if got == nil || got.Code != tc.want {
			t.Fatalf("exception=%+v, want %s", got, tc.want)
		}
	}
}

func (f financialPageFunc) AdminFinancialAuditPage(ctx context.Context, r *purserpb.AdminFinancialAuditPageRequest) (*purserpb.AdminFinancialAuditPageResponse, error) {
	return f(ctx, r)
}

type financialTenantPageFunc func(context.Context, *commonpb.CursorPaginationRequest) (*quartermasterpb.ListTenantsResponse, error)

func (f financialTenantPageFunc) ListTenants(ctx context.Context, r *commonpb.CursorPaginationRequest) (*quartermasterpb.ListTenantsResponse, error) {
	return f(ctx, r)
}

func TestFinancialAuditPreservesEveryPageAndEvidenceHashes(t *testing.T) {
	from, through := time.Unix(0, 0), time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	client := financialPageFunc(func(_ context.Context, r *purserpb.AdminFinancialAuditPageRequest) (*purserpb.AdminFinancialAuditPageResponse, error) {
		if r.Source != "invoices" {
			return &purserpb.AdminFinancialAuditPageResponse{}, nil
		}
		if r.TenantId != "tenant-1" || !r.Through.AsTime().Equal(through) {
			t.Fatalf("wrong audit scope: %+v", r)
		}
		if r.AfterId == "" {
			return &purserpb.AdminFinancialAuditPageResponse{RecordsJson: [][]byte{[]byte(`{"id":"invoice-1","status":"paid","document_kind":"invoice","amount":"100.00"}`)}, NextCursor: "page-two"}, nil
		}
		return &purserpb.AdminFinancialAuditPageResponse{RecordsJson: [][]byte{[]byte(`{"id":"invoice-2","status":"paid","document_kind":"invoice","amount":"200.00"}`)}}, nil
	})
	var out bytes.Buffer
	if err := writeFinancialAudit(context.Background(), &out, client, []string{"tenant-1"}, from, through, false); err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, f := range archive.File {
		r, openErr := f.Open()
		if openErr != nil {
			t.Fatal(openErr)
		}
		files[f.Name], err = io.ReadAll(r)
		_ = r.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	var manifest financialAuditManifest
	if err = json.Unmarshal(files["manifest.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.PostingReady || manifest.Counts["invoices"] != 2 {
		t.Fatalf("manifest=%+v", manifest)
	}
	for name, want := range manifest.SHA256 {
		sum := sha256.Sum256(files[name])
		if got := hex.EncodeToString(sum[:]); got != want {
			t.Fatalf("%s digest=%s want=%s", name, got, want)
		}
	}
	if !bytes.Contains(files["exceptions.csv"], []byte("UNKNOWN_TAX_TREATMENT")) {
		t.Fatal("missing historic VAT was silently accepted")
	}
	if count := bytes.Count(files["exceptions.csv"], []byte("\nUNKNOWN_TAX_TREATMENT,")); count != 1 {
		t.Fatalf("summary must group repeated missing-tax warnings, got %d rows", count)
	}
	if count := bytes.Count(files["exception_records.csv"], []byte("\nUNKNOWN_TAX_TREATMENT,")); count != 2 {
		t.Fatalf("detail must retain both invoice identities, got %d rows", count)
	}
	if _, ok := files["journal.csv"]; ok {
		t.Fatal("unreviewed evidence archive contained a posting feed")
	}
}

func TestFinancialAuditStopsOnPageFailureAndRepeatedCursor(t *testing.T) {
	for _, broken := range []string{"request_failure", "repeated_cursor"} {
		t.Run(broken, func(t *testing.T) {
			client := financialPageFunc(func(_ context.Context, r *purserpb.AdminFinancialAuditPageRequest) (*purserpb.AdminFinancialAuditPageResponse, error) {
				if broken == "request_failure" {
					return nil, errors.New("missing provider page")
				}
				return &purserpb.AdminFinancialAuditPageResponse{NextCursor: "stuck"}, nil
			})
			var out bytes.Buffer
			if err := writeFinancialAudit(context.Background(), &out, client, []string{"tenant"}, time.Unix(0, 0), time.Now(), false); err == nil {
				t.Fatal("incomplete evidence archive accepted")
			}
		})
	}
}

func TestFinancialAuditEnumeratesTenantPages(t *testing.T) {
	client := financialTenantPageFunc(func(_ context.Context, r *commonpb.CursorPaginationRequest) (*quartermasterpb.ListTenantsResponse, error) {
		if r.GetAfter() == "" {
			cursor := "next"
			return &quartermasterpb.ListTenantsResponse{Tenants: []*quartermasterpb.Tenant{{Id: "one"}}, Pagination: &commonpb.CursorPaginationResponse{HasNextPage: true, EndCursor: &cursor}}, nil
		}
		return &quartermasterpb.ListTenantsResponse{Tenants: []*quartermasterpb.Tenant{{Id: "two"}}}, nil
	})
	ids, err := listFinancialAuditTenants(context.Background(), client)
	if err != nil || len(ids) != 2 || ids[1] != "two" {
		t.Fatalf("ids=%v err=%v", ids, err)
	}
}

func TestFinancialAuditIntervalRejectsFutureAndReversedCutoffs(t *testing.T) {
	now := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	for _, pair := range [][2]string{{"2026-11-06", "now"}, {"inception", "2026-11-06T00:00:00Z"}, {"nonsense", "now"}} {
		if _, _, err := financialAuditInterval(pair[0], pair[1], now); err == nil {
			t.Fatalf("accepted %v", pair)
		}
	}
}

func TestFinancialAuditDefaultCutoffUsesOneServerTime(t *testing.T) {
	clientNow := time.Date(2026, 11, 5, 12, 0, 2, 0, time.UTC)
	from, through, err := financialAuditInterval("inception", "now", clientNow)
	if err != nil {
		t.Fatal(err)
	}
	if !through.IsZero() {
		t.Fatalf("default cutoff must be resolved by server, not client clock: %v", through)
	}
	serverNow := clientNow.Add(-2 * time.Second)
	requests := 0
	client := financialPageFunc(func(_ context.Context, r *purserpb.AdminFinancialAuditPageRequest) (*purserpb.AdminFinancialAuditPageResponse, error) {
		if requests == 0 {
			if r.GetThrough() != nil {
				t.Fatal("first request must omit client cutoff")
			}
		} else if r.GetThrough() == nil || !r.GetThrough().AsTime().Equal(serverNow) {
			t.Fatalf("later pages changed the server cutoff: %v", r.GetThrough())
		}
		requests++
		return &purserpb.AdminFinancialAuditPageResponse{EffectiveThrough: timestamppb.New(serverNow), CapturedAt: timestamppb.New(serverNow)}, nil
	})
	var out bytes.Buffer
	if writeErr := writeFinancialAudit(context.Background(), &out, client, []string{"tenant"}, from, through, false); writeErr != nil {
		t.Fatal(writeErr)
	}
	archive, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range archive.File {
		if f.Name != "manifest.json" {
			continue
		}
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		var manifest financialAuditManifest
		if err := json.NewDecoder(r).Decode(&manifest); err != nil {
			t.Fatal(err)
		}
		if !manifest.Through.Equal(serverNow) {
			t.Fatalf("manifest cutoff=%v, want server %v", manifest.Through, serverNow)
		}
		return
	}
	t.Fatal("manifest missing")
}

func TestFinancialAuditFlagsUnknownOccurrenceDates(t *testing.T) {
	for _, tc := range []struct {
		source, record string
		unknown        bool
	}{
		{"balance_transactions", `{"id":"movement","created_at":null}`, true},
		{"credit_notes", `{"id":"note","issued_at":null}`, true},
		{"invoices", `{"id":"invoice","created_at":null,"finalized_at":null}`, true},
		{"invoices", `{"id":"invoice","created_at":null,"finalized_at":"2026-11-01T00:00:00Z"}`, false},
		{"balance_transactions", `{"id":"movement","created_at":"2026-11-01T00:00:00Z"}`, false},
	} {
		var data map[string]json.RawMessage
		if err := json.Unmarshal([]byte(tc.record), &data); err != nil {
			t.Fatal(err)
		}
		e := auditDateException(tc.source, "tenant", data)
		if (e != nil) != tc.unknown || (e != nil && (e.Code != "OCCURRENCE_DATE_UNKNOWN" || e.RecordID == "" || e.TenantID != "tenant")) {
			t.Fatalf("source %s record %s exception=%+v", tc.source, tc.record, e)
		}
	}
}

func TestFinancialAuditCapturesProviderAccountAndTestMode(t *testing.T) {
	through := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	client := financialPageFunc(func(_ context.Context, r *purserpb.AdminFinancialAuditPageRequest) (*purserpb.AdminFinancialAuditPageResponse, error) {
		if strings.HasPrefix(r.Source, "stripe_") && r.TenantId != "" {
			t.Fatal("provider account evidence incorrectly restricted to a customer tenant")
		}
		page := &purserpb.AdminFinancialAuditPageResponse{CapturedAt: timestamppb.New(through)}
		switch r.Source {
		case "stripe_account":
			page.RecordsJson = [][]byte{[]byte(`{"id":"acct_test","object":"account"}`)}
		case "stripe_balance_snapshot":
			page.RecordsJson = [][]byte{[]byte(`{"object":"balance","livemode":false,"available":[],"pending":[]}`)}
		}
		return page, nil
	})
	var out bytes.Buffer
	if err := writeFinancialAudit(context.Background(), &out, client, []string{"tenant-1"}, time.Unix(0, 0), through, true); err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range archive.File {
		if file.Name != "manifest.json" {
			continue
		}
		reader, openErr := file.Open()
		if openErr != nil {
			t.Fatal(openErr)
		}
		defer reader.Close()
		var manifest financialAuditManifest
		if err = json.NewDecoder(reader).Decode(&manifest); err != nil {
			t.Fatal(err)
		}
		if manifest.ProviderAccountID != "acct_test" || manifest.ProviderLiveMode == nil || *manifest.ProviderLiveMode || len(manifest.Observations) == 0 || manifest.Observations[0].CapturedAt == nil {
			t.Fatalf("provider scope/capture missing: %+v", manifest)
		}
		return
	}
	t.Fatal("manifest missing")
}

func TestFinancialAuditRequiresReviewForUnsettledCryptoTaxEvidence(t *testing.T) {
	for _, test := range []struct{ policy, assessment, want string }{
		{"vat-at-confirmed-topup-v2", `{"review_required":true}`, "CRYPTO_TAX_REVIEW_REQUIRED"},
		{"vat-at-confirmed-topup-v2", `{}`, "CRYPTO_TAX_ASSESSMENT_MISSING"},
		{"vat-at-confirmed-topup-v1", `{}`, "LEGACY_CRYPTO_TAX_REVIEW"},
	} {
		row := map[string]json.RawMessage{"id": json.RawMessage(`"document-id"`), "tax_policy_ref": json.RawMessage(`"` + test.policy + `"`), "tax_assessment": json.RawMessage(test.assessment)}
		got := auditTaxException("crypto_invoices", "tenant-id", row)
		if got == nil || got.Code != test.want || got.RecordID != "document-id" {
			t.Fatalf("tax evidence warning=%+v want=%s", got, test.want)
		}
	}
}

func TestFinancialAuditDoesNotCallAnUnknownPolicyLegacy(t *testing.T) {
	row := map[string]json.RawMessage{"id": json.RawMessage(`"document-id"`), "tax_policy_ref": json.RawMessage(`"vat-at-confirmed-topup-v99"`), "tax_assessment": json.RawMessage(`{"policy_version":"vat-at-confirmed-topup-v99"}`)}
	got := auditTaxException("crypto_invoices", "tenant-id", row)
	if got == nil || got.Code != "UNSUPPORTED_CRYPTO_TAX_POLICY" {
		t.Fatalf("unknown policy was misclassified as legacy: %+v", got)
	}
}

func TestProviderFinancialAuditUsesOriginalTaxTotals(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{`{"id":"provider","tax_status":"ready","net_minor":10000,"vat_minor":2100,"gross_minor":12100,"pdf_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`, ""},
		{`{"id":"provider","tax_status":"ready","net_minor":10000,"vat_minor":2100,"gross_minor":99999}`, "TAX_TOTALS_MISMATCH"},
		{`{"id":"provider","tax_status":"legacy_unknown"}`, "PROVIDER_TAX_TREATMENT_UNKNOWN"},
		{`{"id":"provider","tax_status":"ready","net_minor":10000,"vat_minor":2100,"gross_minor":12100}`, "PROVIDER_PDF_PENDING"},
	} {
		var data map[string]json.RawMessage
		if err := json.Unmarshal([]byte(tc.raw), &data); err != nil {
			t.Fatal(err)
		}
		result := auditTaxException("provider_billing_documents", "tenant", data)
		if tc.want == "" {
			if result != nil {
				t.Fatalf("valid imported tax invoice falsely flagged: %+v", result)
			}
		} else if result == nil || result.Code != tc.want {
			t.Fatalf("want%s,got%+v", tc.want, result)
		}
	}
}

func TestFinancialAuditRetainsCanonicalCryptoReviewUntilResolved(t *testing.T) {
	for _, tc := range []struct{ name, assessment, revalidated, want string }{
		{"revoked business", `{"review_required":true,"review_reasons":["business_evidence_unavailable"]}`, "", "CRYPTO_TAX_REVIEW_REQUIRED"},
		{"expired policy", `{"review_required":true,"review_reasons":["tax_policy_expired"]}`, "valid", "CRYPTO_TAX_REVIEW_REQUIRED"},
		{"grace pending", `{"review_required":true,"vat_validation_mode":"outage_grace","review_reasons":["vies_outage_grace"]}`, "", "CRYPTO_TAX_REVIEW_REQUIRED"},
		{"grace verified", `{"review_required":true,"vat_validation_mode":"outage_grace","review_reasons":["vies_outage_grace"]}`, "valid", ""},
		{"grace with expired policy", `{"review_required":true,"vat_validation_mode":"outage_grace","review_reasons":["vies_outage_grace","tax_policy_expired"]}`, "valid", "CRYPTO_TAX_REVIEW_REQUIRED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var data map[string]json.RawMessage
			raw := `{"id":"invoice","status":"paid","document_kind":"invoice","tax_status":"ready","tax_policy_version":"exclusive-service-invoice-v1","net_eur_cents":800,"vat_eur_cents":0,"gross_eur_cents":800,"tax_decision":` + tc.assessment + `,"vies_revalidation":"` + tc.revalidated + `"}`
			if err := json.Unmarshal([]byte(raw), &data); err != nil {
				t.Fatal(err)
			}
			got := auditTaxException("invoices", "tenant", data)
			if tc.want == "" {
				if got != nil {
					t.Fatalf("resolved grace kept a pending exception: %+v", got)
				}
			} else if got == nil || got.Code != tc.want {
				t.Fatalf("canonical crypto review silently passed ready-invoice audit: got=%+v want=%s", got, tc.want)
			}
		})
	}
}
