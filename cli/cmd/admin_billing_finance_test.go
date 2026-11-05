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
