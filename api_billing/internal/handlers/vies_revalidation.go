package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

type billingTenantLister interface {
	ListTenants(context.Context, *commonpb.CursorPaginationRequest) (*quartermasterpb.ListTenantsResponse, error)
}

func (jm *JobManager) runVATRevalidation(ctx context.Context) {
	if jm.billing == nil || jm.taxHandler == nil {
		return
	}
	lister, ok := jm.billing.qmClient.(billingTenantLister)
	if !ok {
		return
	}
	tick := time.NewTicker(5 * time.Minute)
	defer tick.Stop()
	for {
		if err := jm.revalidateTenantVATPages(ctx, lister); err != nil {
			jm.logger.WithError(err).Warn("Billing tax synchronization cycle incomplete")
		}
		select {
		case <-ctx.Done():
			return
		case <-jm.stopCh:
			return
		case <-tick.C:
		}
	}
}

func (jm *JobManager) revalidateTenantVATPages(ctx context.Context, lister billingTenantLister) error {
	cursor := ""
	for {
		page, err := lister.ListTenants(ctx, &commonpb.CursorPaginationRequest{First: 100, After: &cursor})
		if err != nil {
			return err
		}
		for _, tenant := range page.GetTenants() {
			if tenant.GetId() == "" {
				return fmt.Errorf("tenant listing omitted identity")
			}
			if err = jm.taxHandler.revalidateTenantVAT(ctx, tenant.GetId()); err != nil {
				jm.logger.WithError(err).WithField("tenant_id", tenant.GetId()).Warn("VAT evidence revalidation incomplete")
			}
			if err = jm.reconcileStripeTaxTenant(ctx, tenant.GetId()); err != nil {
				jm.logger.WithError(err).WithField("tenant_id", tenant.GetId()).Warn("Stripe billing tax synchronization incomplete")
			}
			if err = jm.reconcileMollieTaxTenant(ctx, tenant.GetId()); err != nil {
				jm.logger.WithError(err).WithField("tenant_id", tenant.GetId()).Warn("Mollie billing tax synchronization incomplete")
			}
			if err = jm.billing.reassessStripeTaxDocuments(ctx, tenant.GetId()); err != nil {
				jm.logger.WithError(err).WithField("tenant_id", tenant.GetId()).Warn("Retained Stripe document tax review incomplete")
			}
		}
		if !page.GetPagination().GetHasNextPage() {
			return nil
		}
		next := page.GetPagination().GetEndCursor()
		if next == "" || next == cursor {
			return fmt.Errorf("tenant listing did not advance")
		}
		cursor = next
	}
}

// Revalidation records later evidence beside the accepted claim. It never
// replaces an issued document or collects another payment from the customer.
func (h *X402Handler) revalidateTenantVAT(ctx context.Context, tenant string) error {
	q := purserdb.New(h.db)
	work, err := q.ListTenantVIESRevalidations(ctx, tenant)
	if err != nil {
		return err
	}
	for _, row := range work {
		var claim struct {
			Country   string `json:"country"`
			VATNumber string `json:"vat_number"`
		}
		if err = json.Unmarshal(row.EvidenceJson, &claim); err != nil {
			return err
		}
		if claim.Country == "" || !strings.HasPrefix(claim.VATNumber, claim.Country) {
			return fmt.Errorf("invalid retained VIES work identity")
		}
		digest := sha256.Sum256([]byte(claim.VATNumber))
		proof, refreshErr := h.refreshVIESVAT(ctx, tenant, claim.Country, strings.TrimPrefix(claim.VATNumber, claim.Country), hex.EncodeToString(digest[:]))
		result := map[string]any{}
		verified := false
		if refreshErr == nil {
			verified = proof.Valid
			result["revalidation_outcome"] = map[bool]string{true: "valid", false: "invalid"}[verified]
			result["revalidated_evidence"] = proof.Snapshot
		} else {
			var temporary *viesUnavailableError
			if !errors.As(refreshErr, &temporary) {
				result["revalidation_outcome"] = "requires_review"
			}
		}
		encoded, encodeErr := json.Marshal(result)
		if encodeErr != nil {
			return encodeErr
		}
		if err = q.RecordVIESRevalidationResult(ctx, purserdb.RecordVIESRevalidationResultParams{TenantID: tenant, ID: row.ID, Result: encoded, Verified: verified}); err != nil {
			return err
		}
	}
	return nil
}
