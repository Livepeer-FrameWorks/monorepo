package handlers

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	billingstripe "frameworks/api_billing/internal/stripe"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/countries"
)

type stripeBusinessProof struct {
	TenantID   string    `json:"tenant_id"`
	ProfileSHA string    `json:"profile_sha256"`
	Country    string    `json:"country"`
	Reference  string    `json:"reference"`
	CreatedAt  time.Time `json:"created_at"`
	ValidUntil time.Time `json:"valid_until"`
	Revoked    bool      `json:"is_revocation"`
}

func (s *Service) stripeBusinessEvidenceAtIssue(ctx context.Context, tenant string, doc billingstripe.ProviderTaxDocument, profile billingstripe.TaxProfile, proof CryptoTaxAssessment) ([]string, error) {
	if len(proof.Decision.BusinessEstablishment) == 0 {
		if proof.Decision.Treatment == "non_eu_b2b" {
			return []string{"business_evidence_missing_at_tax_point"}, nil
		}
		return nil, nil
	}
	point := doc.IssuedAt.Add(time.Second - time.Nanosecond)
	var accepted stripeBusinessProof
	if json.Unmarshal(proof.Decision.BusinessEstablishment, &accepted) != nil {
		return []string{"business_evidence_invalid_at_tax_point"}, nil //nolint:nilerr // Invalid immutable evidence requires review, not an operational retry.
	}
	digest, hashErr := hex.DecodeString(accepted.ProfileSHA)
	if hashErr != nil || len(digest) != 32 || accepted.TenantID != tenant || countries.Normalize(accepted.Country) != countries.Normalize(profile.Country) || strings.TrimSpace(accepted.Reference) == "" || accepted.CreatedAt.IsZero() || accepted.CreatedAt.After(point) || accepted.Revoked {
		return []string{"business_evidence_invalid_at_tax_point"}, nil //nolint:nilerr // A malformed accepted binding is a permanent review outcome.
	}
	// The accepted full-profile hash includes fields absent from TaxProfile
	// (such as state). Its immutable review is the authoritative binding.
	row, err := purserdb.New(s.db).GetBusinessEstablishmentReviewAtTaxPoint(ctx, purserdb.GetBusinessEstablishmentReviewAtTaxPointParams{TenantID: tenant, ProfileSha256: accepted.ProfileSHA, Country: accepted.Country, ValidAt: point})
	if errors.Is(err, sql.ErrNoRows) {
		return []string{"business_evidence_missing_at_tax_point"}, nil
	}
	if err != nil {
		return nil, err
	}
	var historical stripeBusinessProof
	if json.Unmarshal(row, &historical) != nil || historical.Revoked || historical.TenantID != tenant || historical.ProfileSHA != accepted.ProfileSHA || countries.Normalize(historical.Country) != countries.Normalize(profile.Country) || strings.TrimSpace(historical.Reference) == "" || historical.CreatedAt.IsZero() || historical.CreatedAt.After(point) || !doc.IssuedAt.Before(historical.ValidUntil) {
		return []string{"business_evidence_invalid_at_tax_point"}, nil //nolint:nilerr // Invalid historical proof remains reviewable without retrying immutable facts.
	}
	return nil, nil
}
