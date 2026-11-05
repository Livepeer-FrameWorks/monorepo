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
	"strconv"
	"strings"
	"time"

	billingcontract "github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var localFinancialAuditSources = []string{"invoices", "payments", "simplified_invoices", "crypto_invoices", "credit_notes", "provider_settlements", "pending_topups", "balance_transactions", "business_establishment_reviews", "provider_billing_documents", "provider_tax_profiles"}
var stripeFinancialAuditSources = []string{"stripe_account", "stripe_balance_snapshot", "stripe_tax_settings", "stripe_subscriptions", "stripe_customers", "stripe_invoices", "stripe_charges", "stripe_balance_transactions", "stripe_payouts"}

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
	ExceptionGroups   int                         `json:"exception_group_count"`
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
	cmd.AddCommand(newAdminBillingTaxProfileCmd(), newAdminRecordBusinessEvidenceCmd())
	return cmd
}

func newAdminBillingTaxProfileCmd() *cobra.Command {
	var tenant string
	cmd := &cobra.Command{Use: "tax-profile", Short: "Read the billing-profile fingerprint for an operator establishment review", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, cfg, cleanup, err := purserGRPCClientFromContext(cmd.Context())
			if err != nil {
				return err
			}
			defer cleanup()
			defer p.Close()
			ctx := context.WithValue(cmd.Context(), ctxkeys.KeyJWTToken, cfg.Auth.JWT)
			page, err := p.AdminFinancialAuditPage(ctx, &purserpb.AdminFinancialAuditPageRequest{TenantId: tenant, Source: "tax_profile", From: timestamppb.New(time.Unix(0, 0).UTC())})
			if err != nil {
				return err
			}
			if len(page.GetRecordsJson()) != 1 {
				return errors.New("purser returned no billing-profile fingerprint")
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), string(page.GetRecordsJson()[0]))
			return err
		}}
	cmd.Flags().StringVar(&tenant, "tenant-id", "", "Customer tenant UUID whose establishment evidence is being reviewed")
	if err := cmd.MarkFlagRequired("tenant-id"); err != nil {
		panic(err)
	}
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
	if through == "now" {
		return start.UTC(), time.Time{}, nil
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
	manifest := financialAuditManifest{SchemaVersion: 2, From: from, Through: through, CapturedAt: time.Now().UTC(), TenantIDs: tenants, Counts: map[string]int{}, SHA256: map[string]string{}, Scope: "Quartermaster-visible tenants plus configured Stripe account; occurrence cutoff with current status snapshots"}
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
				var cutoff *timestamppb.Timestamp
				if !through.IsZero() {
					cutoff = timestamppb.New(through)
				}
				page, err := client.AdminFinancialAuditPage(ctx, &purserpb.AdminFinancialAuditPageRequest{TenantId: tenantID, Source: source, AfterId: cursor, From: timestamppb.New(from), Through: cutoff})
				if err != nil {
					return fmt.Errorf("financial evidence %s tenant %s: %w", source, tenantID, err)
				}
				if page == nil {
					return fmt.Errorf("financial evidence %s returned no page", source)
				}
				if through.IsZero() {
					resolved := page.GetEffectiveThrough()
					if resolved == nil || resolved.CheckValid() != nil || !resolved.AsTime().After(from) {
						return errors.New("purser did not resolve the audit cutoff; matching CLI and server versions are required")
					}
					through = resolved.AsTime().UTC()
					manifest.Through = through
				} else if resolved := page.GetEffectiveThrough(); resolved != nil && (resolved.CheckValid() != nil || !resolved.AsTime().Equal(through)) {
					return errors.New("financial evidence page changed the fixed audit cutoff")
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
	groups := map[[4]string]int{}
	var order [][4]string
	var details [][]string
	for _, e := range exceptions {
		key := [4]string{e.Code, e.Source, e.TenantID, e.Message}
		if groups[key] == 0 {
			order = append(order, key)
		}
		groups[key]++
		details = append(details, []string{e.Code, e.Source, e.TenantID, e.RecordID, e.Message})
	}
	var summary [][]string
	for _, key := range order {
		summary = append(summary, []string{key[0], key[1], key[2], strconv.Itoa(groups[key]), key[3]})
	}
	for _, table := range []struct {
		name   string
		header []string
		rows   [][]string
	}{
		{"exceptions.csv", []string{"code", "source", "tenant_id", "record_count", "required_action"}, summary},
		{"exception_records.csv", []string{"code", "source", "tenant_id", "record_id", "required_action"}, details},
	} {
		entry, err := archive.Create(table.name)
		if err != nil {
			return err
		}
		hash := sha256.New()
		csvWriter := csv.NewWriter(io.MultiWriter(entry, hash))
		if err := csvWriter.Write(table.header); err != nil {
			return err
		}
		for _, row := range table.rows {
			for i := range row {
				row[i] = financialAuditCSVCell(row[i])
			}
			if err := csvWriter.Write(row); err != nil {
				return err
			}
		}
		csvWriter.Flush()
		if err := csvWriter.Error(); err != nil {
			return err
		}
		manifest.SHA256[table.name] = hex.EncodeToString(hash.Sum(nil))
	}
	manifest.Exceptions = len(exceptions)
	manifest.ExceptionGroups = len(groups)
	manifest.CompletedAt = time.Now().UTC()
	entry, err := archive.Create("manifest.json")
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
	if source == "provider_billing_documents" {
		var assessment struct {
			Evidence struct {
				Mode string `json:"vat_validation_mode"`
			} `json:"evidence"`
		}
		if json.Unmarshal(data["tax_assessment"], &assessment) == nil && assessment.Evidence.Mode == "outage_grace" && auditStringField(data, "vies_revalidation") != "valid" {
			return &financialAuditException{Code: "PROVIDER_VAT_REVALIDATION_REQUIRED", Source: source, TenantID: tenant, RecordID: id, Message: "The provider document retained outage-grace VAT proof; automatic revalidation is pending or requires correction review."}
		}
		if auditStringField(data, "tax_status") != "ready" {
			return &financialAuditException{Code: "PROVIDER_TAX_TREATMENT_UNKNOWN", Source: source, TenantID: tenant, RecordID: id, Message: "Original provider document does not state a reliable net/VAT split; retain it for accounting review."}
		}
		var net, vat, gross int64
		if json.Unmarshal(data["net_minor"], &net) != nil || json.Unmarshal(data["vat_minor"], &vat) != nil || json.Unmarshal(data["gross_minor"], &gross) != nil || string(data["net_minor"]) == "null" || string(data["vat_minor"]) == "null" || net+vat != gross {
			return &financialAuditException{Code: "TAX_TOTALS_MISMATCH", Source: source, TenantID: tenant, RecordID: id, Message: "Original provider net/VAT/gross totals are missing or inconsistent."}
		}
		if len(auditStringField(data, "pdf_sha256")) != 64 {
			return &financialAuditException{Code: "PROVIDER_PDF_PENDING", Source: source, TenantID: tenant, RecordID: id, Message: "The original provider PDF is awaiting capture; retry the provider inbox before accounting sign-off."}
		}
		return nil
	}
	if source == "invoices" && status != "draft" && status != "manual_review" && kind != "prepaid_statement" {
		var retainedReview struct {
			ReviewRequired bool     `json:"review_required"`
			ReviewReasons  []string `json:"review_reasons"`
			ValidationMode string   `json:"vat_validation_mode"`
		}
		if json.Unmarshal(data["tax_decision"], &retainedReview) == nil && retainedReview.ReviewRequired {
			verifiedGrace := retainedReview.ValidationMode == "outage_grace" && len(retainedReview.ReviewReasons) == 1 && retainedReview.ReviewReasons[0] == "vies_outage_grace" && auditStringField(data, "vies_revalidation") == "valid"
			if !verifiedGrace {
				return &financialAuditException{Code: "CRYPTO_TAX_REVIEW_REQUIRED", Source: source, TenantID: tenant, RecordID: id, Message: "Payment retained its accepted tax claim with expired, revoked or outage evidence; review the retained assessment and follow-up evidence before accounting sign-off."}
			}
		}
		if auditStringField(data, "tax_status") == "ready" || auditStringField(data, "tax_status") == "no_supply" {
			var net, vat, gross int64
			if json.Unmarshal(data["net_eur_cents"], &net) != nil || json.Unmarshal(data["vat_eur_cents"], &vat) != nil || json.Unmarshal(data["gross_eur_cents"], &gross) != nil || string(data["net_eur_cents"]) == "null" || string(data["vat_eur_cents"]) == "null" || string(data["gross_eur_cents"]) == "null" || net < 0 || vat < 0 || net+vat != gross {
				return &financialAuditException{Code: "TAX_TOTALS_MISMATCH", Source: source, TenantID: tenant, RecordID: id, Message: "Frozen invoice net/VAT/gross totals are missing or inconsistent; do not post this invoice."}
			}
			if auditStringField(data, "tax_policy_version") != billingcontract.CurrentInvoiceTaxPolicy {
				return &financialAuditException{Code: "UNSUPPORTED_INVOICE_TAX_POLICY", Source: source, TenantID: tenant, RecordID: id, Message: "Use a CLI that supports the invoice's tax policy before interpreting it."}
			}
			return nil
		}
		if net, ok := data["net_eur_cents"]; !ok || string(net) == "null" {
			return &financialAuditException{Code: "UNKNOWN_TAX_TREATMENT", Source: source, TenantID: tenant, RecordID: id, Message: "Review the original invoice tax breakdown and agreed net/inclusive price; do not infer VAT from the amount due."}
		}
	}
	if source == "simplified_invoices" || source == "crypto_invoices" {
		var assessment struct {
			ReviewRequired bool `json:"review_required"`
		}
		if json.Unmarshal(data["tax_assessment"], &assessment) == nil && assessment.ReviewRequired {
			return &financialAuditException{Code: "CRYPTO_TAX_REVIEW_REQUIRED", Source: source, TenantID: tenant, RecordID: id, Message: "Payment confirmed with expired, revoked or unavailable tax evidence; retain the original assessment and review any required correction."}
		}
		if billingcontract.IsLegacyCryptoTaxPolicy(auditStringField(data, "tax_policy_ref")) {
			return &financialAuditException{Code: "LEGACY_CRYPTO_TAX_REVIEW", Source: source, TenantID: tenant, RecordID: id, Message: "Legacy treatment is retained; verify the original customer and tax evidence before accounting sign-off or corrections."}
		}
		if !billingcontract.IsSupportedCryptoTaxPolicy(auditStringField(data, "tax_policy_ref")) {
			return &financialAuditException{Code: "UNSUPPORTED_CRYPTO_TAX_POLICY", Source: source, TenantID: tenant, RecordID: id, Message: "Use a CLI version that supports the document's tax policy before interpreting this evidence."}
		}
		if assessment := data["tax_assessment"]; len(assessment) == 0 || string(assessment) == "null" || string(assessment) == "{}" {
			return &financialAuditException{Code: "CRYPTO_TAX_ASSESSMENT_MISSING", Source: source, TenantID: tenant, RecordID: id, Message: "The issued document lacks its frozen tax assessment; recover the original payment evidence before accounting sign-off."}
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
	case "simplified_invoices", "crypto_invoices", "credit_notes", "provider_billing_documents":
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

func financialAuditCSVCell(value string) string {
	value = strings.ReplaceAll(strings.ReplaceAll(value, "\r", " "), "\n", " ")
	trimmed := strings.TrimLeft(value, " ")
	if strings.HasPrefix(trimmed, "=") || strings.HasPrefix(trimmed, "+") || strings.HasPrefix(trimmed, "-") || strings.HasPrefix(trimmed, "@") || strings.HasPrefix(value, "\t") {
		return "'" + value
	}
	return value
}

func newAdminRecordBusinessEvidenceCmd() *cobra.Command {
	var tenant, fingerprint, reference, until string
	var revoke bool
	var expectedRevision int64
	cmd := &cobra.Command{Use: "record-business-evidence", Short: "Record a reviewed non-EU business proof against an unchanged billing profile", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var expiry *timestamppb.Timestamp
			if revoke {
				if fingerprint != "" || until != "" {
					return errors.New("--revoke omits --profile-sha256 and --valid-until")
				}
			} else {
				if fingerprint == "" || until == "" || !cmd.Flags().Changed("expected-revision") {
					return errors.New("--profile-sha256, --expected-revision and --valid-until are required for an approval")
				}
				parsed, err := time.Parse(time.RFC3339, until)
				if err != nil {
					return fmt.Errorf("invalid --valid-until: %w", err)
				}
				expiry = timestamppb.New(parsed)
			}
			p, cfg, cleanup, err := purserGRPCClientFromContext(cmd.Context())
			if err != nil {
				return err
			}
			defer cleanup()
			defer p.Close()
			ctx := context.WithValue(cmd.Context(), ctxkeys.KeyJWTToken, cfg.Auth.JWT)
			recorded, err := p.AdminRecordBusinessEvidence(ctx, &purserpb.AdminRecordBusinessEvidenceRequest{TenantId: tenant, ProfileSha256: fingerprint, Reference: reference, ValidUntil: expiry, Revoke: revoke, ExpectedRevision: expectedRevision})
			if err != nil {
				return err
			}
			raw, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(recorded)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), string(raw))
			return err
		}}
	cmd.Flags().StringVar(&tenant, "tenant-id", "", "Customer tenant UUID")
	cmd.Flags().StringVar(&fingerprint, "profile-sha256", "", "Fingerprint of the exact billing profile reviewed by the operator")
	cmd.Flags().StringVar(&reference, "reference", "", "Reference to retained establishment evidence; this command does not upload documents")
	cmd.Flags().StringVar(&until, "valid-until", "", "RFC3339 evidence expiry within the next 366 days")
	cmd.Flags().Int64Var(&expectedRevision, "expected-revision", 0, "Evidence revision returned with the reviewed profile")
	cmd.Flags().BoolVar(&revoke, "revoke", false, "Revoke all earlier establishment approvals for this tenant")
	for _, flag := range []string{"tenant-id", "reference"} {
		if err := cmd.MarkFlagRequired(flag); err != nil {
			panic(err)
		}
	}
	return cmd
}
