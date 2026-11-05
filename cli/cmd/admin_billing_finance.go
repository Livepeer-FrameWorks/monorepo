package cmd

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var localFinancialAuditSources = []string{"invoices", "payments", "simplified_invoices", "crypto_invoices", "credit_notes", "provider_settlements", "pending_topups", "balance_transactions"}
var stripeFinancialAuditSources = []string{"stripe_account", "stripe_balance_snapshot", "stripe_invoices", "stripe_charges", "stripe_balance_transactions", "stripe_payouts"}

type financialAuditClient interface {
	AdminFinancialAuditPage(context.Context, *purserpb.AdminFinancialAuditPageRequest) (*purserpb.AdminFinancialAuditPageResponse, error)
}

type financialAuditTenants interface {
	ListTenants(context.Context, *commonpb.CursorPaginationRequest) (*quartermasterpb.ListTenantsResponse, error)
}

type financialAuditException struct{ Code, Source, TenantID, RecordID, Message string }

type financialAuditObservation struct {
	Source     string     `json:"source"`
	TenantID   string     `json:"tenant_id,omitempty"`
	AfterID    string     `json:"after_id,omitempty"`
	CapturedAt *time.Time `json:"captured_at"`
}

type financialAuditManifest struct {
	SchemaVersion     int                         `json:"schema_version"`
	From              time.Time                   `json:"occurrence_from"`
	Through           time.Time                   `json:"occurrence_through_exclusive"`
	CapturedAt        time.Time                   `json:"captured_at"`
	CompletedAt       time.Time                   `json:"completed_at"`
	Observations      []financialAuditObservation `json:"page_observations"`
	PostingReady      bool                        `json:"posting_ready"`
	TenantIDs         []string                    `json:"tenant_ids"`
	Counts            map[string]int              `json:"record_counts"`
	SHA256            map[string]string           `json:"sha256"`
	Exceptions        int                         `json:"exception_count"`
	Scope             string                      `json:"scope"`
	ProviderAccountID string                      `json:"provider_account_id,omitempty"`
	ProviderLiveMode  *bool                       `json:"provider_livemode,omitempty"`
}

func newAdminBillingFinanceCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "finance", Short: "Operator financial evidence and accounting preparation"}
	var from, through, destination, provider string
	audit := &cobra.Command{Use: "audit", Short: "Download read-only Purser and Stripe evidence; does not certify payout readiness", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			start, end, err := financialAuditInterval(from, through, time.Now())
			if err != nil {
				return err
			}
			if provider != "stripe" && provider != "none" {
				return errors.New("provider must be stripe or none")
			}
			p, cfg, cleanup, err := purserGRPCClientFromContext(cmd.Context())
			if err != nil {
				return err
			}
			defer cleanup()
			defer p.Close()
			qm, _, qmCleanup, err := qmGRPCClientFromContext(cmd.Context())
			if err != nil {
				return err
			}
			defer qmCleanup()
			defer qm.Close()
			ctx := context.WithValue(cmd.Context(), ctxkeys.KeyJWTToken, cfg.Auth.JWT)
			tenants, err := listFinancialAuditTenants(ctx, qm)
			if err != nil {
				return err
			}
			if len(tenants) == 0 {
				return errors.New("no visible tenants; refusing an apparently empty financial audit")
			}
			if err := writeFinancialAuditFile(ctx, destination, p, tenants, start, end, provider == "stripe"); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Financial evidence saved to %s. Posting-ready: false. Review manifest.json and exceptions.csv; no payouts or collections were performed.\n", destination)
			return nil
		}}
	audit.Flags().StringVar(&from, "from", "inception", "Occurrence start: inception, YYYY-MM-DD (UTC), or RFC3339")
	audit.Flags().StringVar(&through, "through", "now", "Exclusive occurrence cutoff: now or RFC3339")
	audit.Flags().StringVar(&destination, "output", "", "New ZIP file; existing files are never overwritten")
	audit.Flags().StringVar(&provider, "provider", "stripe", "Provider evidence: stripe or none")
	if flagErr := audit.MarkFlagRequired("output"); flagErr != nil {
		panic(flagErr)
	}
	cmd.AddCommand(audit)
	return cmd
}

func financialAuditInterval(from, through string, now time.Time) (time.Time, time.Time, error) {
	start, end := time.Unix(0, 0).UTC(), now.UTC().Truncate(time.Second)
	var err error
	if from != "inception" {
		start, err = time.Parse(time.RFC3339, from)
		if err != nil {
			start, err = time.Parse("2006-01-02", from)
		}
		if err != nil {
			return start, end, fmt.Errorf("invalid --from: %w", err)
		}
	}
	if through != "now" {
		end, err = time.Parse(time.RFC3339, through)
		if err != nil {
			return start, end, fmt.Errorf("invalid --through: %w", err)
		}
	}
	if !end.After(start) || end.After(now) {
		return start, end, errors.New("audit requires from < through <= now")
	}
	return start.UTC(), end.UTC(), nil
}

func listFinancialAuditTenants(ctx context.Context, client financialAuditTenants) ([]string, error) {
	var ids []string
	seen, cursor := map[string]bool{}, ""
	for {
		page, err := client.ListTenants(ctx, &commonpb.CursorPaginationRequest{First: 100, After: &cursor})
		if err != nil {
			return nil, fmt.Errorf("enumerate financial tenant scope: %w", err)
		}
		for _, tenant := range page.GetTenants() {
			if tenant.GetId() == "" || seen[tenant.GetId()] {
				return nil, errors.New("tenant enumeration repeated or omitted an identity")
			}
			seen[tenant.GetId()] = true
			ids = append(ids, tenant.GetId())
		}
		if !page.GetPagination().GetHasNextPage() {
			return ids, nil
		}
		next := page.GetPagination().GetEndCursor()
		if next == "" || next == cursor {
			return nil, errors.New("tenant enumeration cursor did not advance")
		}
		cursor = next
	}
}

func writeFinancialAuditFile(ctx context.Context, destination string, client financialAuditClient, tenants []string, from, through time.Time, includeStripe bool) error {
	if strings.TrimSpace(destination) == "" {
		return errors.New("output file is required")
	}
	f, err := os.CreateTemp(filepath.Dir(destination), ".financial-audit-*.zip")
	if err != nil {
		return err
	}
	temporary := f.Name()
	defer os.Remove(temporary)
	if err = writeFinancialAudit(ctx, f, client, tenants, from, through, includeStripe); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	// Linking publishes only a complete archive and refuses existing targets.
	return os.Link(temporary, destination)
}

func writeFinancialAudit(ctx context.Context, out io.Writer, client financialAuditClient, tenants []string, from, through time.Time, includeStripe bool) error {
	archive := zip.NewWriter(out)
	manifest := financialAuditManifest{SchemaVersion: 1, From: from, Through: through, CapturedAt: time.Now().UTC(), TenantIDs: tenants, Counts: map[string]int{}, SHA256: map[string]string{}, Scope: "Quartermaster-visible tenants plus configured Stripe account; occurrence cutoff with current status snapshots"}
	exceptions := []financialAuditException{
		{Code: "HISTORICAL_TENANT_COVERAGE_UNPROVEN", Message: "Check retained Purser records for tenants no longer enumerated by Quartermaster before certifying a historical close."},
		{Code: "ACCOUNTING_CLOSE_NOT_RECONCILED", Message: "Raw evidence archive only: no balanced journal, original PDF archive, verified prepaid liability schedule, or bank reconciliation is implied."},
	}
	if !includeStripe {
		exceptions = append(exceptions, financialAuditException{Code: "PROVIDER_EVIDENCE_EXCLUDED", Message: "No provider account evidence was requested."})
	}
	sources := append([]string(nil), localFinancialAuditSources...)
	if includeStripe {
		sources = append(sources, stripeFinancialAuditSources...)
	}
	for _, source := range sources {
		seenRecords := map[string]bool{}
		name := "evidence/" + source + ".jsonl"
		entry, err := archive.Create(name)
		if err != nil {
			return err
		}
		hash := sha256.New()
		writer := io.MultiWriter(entry, hash)
		scopes := tenants
		if strings.HasPrefix(source, "stripe_") {
			scopes = []string{""}
		}
		for _, tenantID := range scopes {
			cursor := ""
			seenCursors := map[string]bool{}
			for {
				page, err := client.AdminFinancialAuditPage(ctx, &purserpb.AdminFinancialAuditPageRequest{TenantId: tenantID, Source: source, AfterId: cursor, From: timestamppb.New(from), Through: timestamppb.New(through)})
				if err != nil {
					return fmt.Errorf("financial evidence %s tenant %s: %w", source, tenantID, err)
				}
				if page == nil {
					return fmt.Errorf("financial evidence %s returned no page", source)
				}
				observation := financialAuditObservation{Source: source, TenantID: tenantID, AfterID: cursor}
				if stamp := page.GetCapturedAt(); stamp != nil && stamp.CheckValid() == nil {
					at := stamp.AsTime()
					observation.CapturedAt = &at
				}
				manifest.Observations = append(manifest.Observations, observation)
				for _, record := range page.GetRecordsJson() {
					var data map[string]json.RawMessage
					if err = json.Unmarshal(record, &data); err != nil {
						return fmt.Errorf("invalid %s evidence: %w", source, err)
					}
					if data == nil {
						return fmt.Errorf("invalid %s evidence: null record", source)
					}
					if source == "stripe_account" {
						if decodeErr := json.Unmarshal(data["id"], &manifest.ProviderAccountID); decodeErr != nil {
							return fmt.Errorf("invalid stripe account identity: %w", decodeErr)
						}
					}
					if source == "stripe_balance_snapshot" {
						var mode *bool
						if modeErr := json.Unmarshal(data["livemode"], &mode); modeErr == nil && mode != nil {
							manifest.ProviderLiveMode = mode
						} else {
							exceptions = append(exceptions, financialAuditException{Code: "PROVIDER_MODE_UNVERIFIED", Source: source, Message: "Provider balance evidence omitted its live/test mode."})
						}
					}
					if source != "stripe_balance_snapshot" {
						var id string
						if identityErr := json.Unmarshal(data["id"], &id); identityErr != nil || id == "" || seenRecords[id] {
							return fmt.Errorf("invalid %s evidence: missing or repeated record identity", source)
						}
						seenRecords[id] = true
					}
					var compact bytes.Buffer
					if err = json.Compact(&compact, record); err != nil {
						return err
					}
					if _, err = writer.Write(compact.Bytes()); err != nil {
						return err
					}
					if _, err = io.WriteString(writer, "\n"); err != nil {
						return err
					}
					manifest.Counts[source]++
					if exception := auditTaxException(source, tenantID, data); exception != nil {
						exceptions = append(exceptions, *exception)
					}
					if exception := auditDateException(source, tenantID, data); exception != nil {
						exceptions = append(exceptions, *exception)
					}
				}
				next := page.GetNextCursor()
				if next == "" {
					break
				}
				if next == cursor || seenCursors[next] {
					return fmt.Errorf("financial evidence %s cursor did not advance", source)
				}
				seenCursors[next] = true
				cursor = next
			}
		}
		manifest.SHA256[name] = hex.EncodeToString(hash.Sum(nil))
	}
	entry, err := archive.Create("exceptions.csv")
	if err != nil {
		return err
	}
	hash := sha256.New()
	csvWriter := csv.NewWriter(io.MultiWriter(entry, hash))
	if err = csvWriter.Write([]string{"code", "source", "tenant_id", "record_id", "required_action"}); err != nil {
		return err
	}
	for _, e := range exceptions {
		row := []string{e.Code, e.Source, e.TenantID, e.RecordID, e.Message}
		for i := range row {
			if strings.ContainsAny(strings.TrimLeft(row[i], " "), "\r\n") {
				row[i] = strings.ReplaceAll(strings.ReplaceAll(row[i], "\r", " "), "\n", " ")
			}
			if strings.HasPrefix(strings.TrimLeft(row[i], " "), "=") || strings.HasPrefix(strings.TrimLeft(row[i], " "), "+") || strings.HasPrefix(strings.TrimLeft(row[i], " "), "-") || strings.HasPrefix(strings.TrimLeft(row[i], " "), "@") || strings.HasPrefix(row[i], "\t") {
				row[i] = "'" + row[i]
			}
		}
		if err = csvWriter.Write(row); err != nil {
			return err
		}
	}
	csvWriter.Flush()
	if err = csvWriter.Error(); err != nil {
		return err
	}
	manifest.SHA256["exceptions.csv"] = hex.EncodeToString(hash.Sum(nil))
	manifest.Exceptions = len(exceptions)
	manifest.CompletedAt = time.Now().UTC()
	entry, err = archive.Create("manifest.json")
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(entry)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(manifest); err != nil {
		return err
	}
	return archive.Close()
}

func auditTaxException(source, tenant string, data map[string]json.RawMessage) *financialAuditException {
	id, status, kind := auditStringField(data, "id"), auditStringField(data, "status"), auditStringField(data, "document_kind")
	if source == "invoices" && status != "draft" && status != "manual_review" && kind != "prepaid_statement" {
		if net, ok := data["net_eur_cents"]; !ok || string(net) == "null" {
			return &financialAuditException{Code: "UNKNOWN_TAX_TREATMENT", Source: source, TenantID: tenant, RecordID: id, Message: "Review the original invoice tax breakdown and agreed net/inclusive price; do not infer VAT from the amount due."}
		}
	}
	if source == "stripe_invoices" {
		return &financialAuditException{Code: "STRIPE_TAX_TREATMENT_UNVERIFIED", Source: source, RecordID: id, Message: "Compare the original Stripe tax invoice and customer evidence with the shared tax rules; preserve its original number and PDF."}
	}
	return nil
}

func auditDateException(source, tenant string, data map[string]json.RawMessage) *financialAuditException {
	if strings.HasPrefix(source, "stripe_") {
		return nil
	}
	field := "created_at"
	switch source {
	case "simplified_invoices", "crypto_invoices", "credit_notes":
		field = "issued_at"
	case "invoices":
		if auditStringField(data, "finalized_at") != "" {
			return nil
		}
	}
	if auditStringField(data, field) == "" {
		return &financialAuditException{Code: "OCCURRENCE_DATE_UNKNOWN", Source: source, TenantID: tenant, RecordID: auditStringField(data, "id"), Message: "Retained despite unknown occurrence date; establish its accounting period before using interval control totals."}
	}
	return nil
}

func auditStringField(data map[string]json.RawMessage, key string) string {
	var value string
	if err := json.Unmarshal(data[key], &value); err != nil {
		return ""
	}
	return value
}
