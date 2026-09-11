package placement

import (
	"fmt"
	"slices"
	"strings"
)

// CheckConstraints checks entitlement, ownership and intersected hard constraints
// for an already elected destination. It does not rank preference groups, assert
// capacity/readiness or authorize a new destination election. An explicit empty
// group list still denies all placement.
func CheckConstraints(r Request, c Candidate) (Reason, error) {
	if strings.TrimSpace(r.TenantID) == "" || (r.Verb != Ingest && r.Verb != Serve) || r.Now.IsZero() || c.ClusterID == "" {
		return "", fmt.Errorf("tenant, media verb, cluster and evaluation time are required")
	}
	if err := Validate(r.Policy); err != nil {
		return "", err
	}
	if c.TenantID != r.TenantID || !slices.Contains(c.AllowedVerbs, r.Verb) {
		return NotEntitled, nil
	}
	if candidateClass(c, r.TenantID) == "" {
		return UnknownOwnership, nil
	}
	if r.Policy == nil {
		return Eligible, nil
	}
	if len(r.Policy.Groups) == 0 {
		return PolicyDenied, nil
	}
	return policyReason(r.Policy, c, r), nil
}
