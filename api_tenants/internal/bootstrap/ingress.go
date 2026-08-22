package bootstrap

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"

	"frameworks/api_tenants/internal/database/quartermasterdb"
)

// ReconcileIngress reconciles TLS bundles first (sites reference them via FK),
// then ingress sites. Stable key on both is the manifest-supplied ID.
func ReconcileIngress(ctx context.Context, exec DBTX, ingress IngressSection) (Result, error) {
	if exec == nil {
		return Result{}, errors.New("ReconcileIngress: nil executor")
	}

	res := Result{}

	for _, b := range ingress.TLSBundles {
		if err := validateTLSBundle(b); err != nil {
			return Result{}, err
		}
		action, err := upsertTLSBundle(ctx, exec, b)
		if err != nil {
			return Result{}, fmt.Errorf("tls_bundle %q: %w", b.ID, err)
		}
		appendBundleAction(&res, "tls_bundle:"+b.ID, action)
	}

	for _, s := range ingress.Sites {
		if err := validateIngressSite(s); err != nil {
			return Result{}, err
		}
		action, err := upsertIngressSite(ctx, exec, s)
		if err != nil {
			return Result{}, fmt.Errorf("ingress_site %q: %w", s.ID, err)
		}
		appendBundleAction(&res, "ingress_site:"+s.ID, action)
	}

	return res, nil
}

func appendBundleAction(r *Result, key, action string) {
	switch action {
	case "created":
		r.Created = append(r.Created, key)
	case "updated":
		r.Updated = append(r.Updated, key)
	case "noop":
		r.Noop = append(r.Noop, key)
	}
}

func validateTLSBundle(b TLSBundle) error {
	if b.ID == "" {
		return errors.New("tls_bundle id required")
	}
	if b.ClusterID == "" {
		return fmt.Errorf("tls_bundle %q: cluster_id required", b.ID)
	}
	if len(b.Domains) == 0 {
		return fmt.Errorf("tls_bundle %q: domains required", b.ID)
	}
	if b.Email == "" {
		return fmt.Errorf("tls_bundle %q: email required", b.ID)
	}
	return nil
}

func upsertTLSBundle(ctx context.Context, exec DBTX, b TLSBundle) (string, error) {
	domainsJSON, err := marshalSortedDomains(b.Domains)
	if err != nil {
		return "", fmt.Errorf("encode domains: %w", err)
	}
	issuer := b.Issuer
	if issuer == "" {
		issuer = "navigator"
	}

	queries := quartermasterdb.New(exec)
	current, err := queries.GetBootstrapTLSBundle(ctx, b.ID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if insertErr := queries.InsertBootstrapTLSBundle(ctx, quartermasterdb.InsertBootstrapTLSBundleParams{
			BundleID: b.ID, ClusterID: b.ClusterID, Domains: domainsJSON, Issuer: issuer, Email: b.Email,
		}); insertErr != nil {
			return "", fmt.Errorf("insert: %w", insertErr)
		}
		return "created", nil
	case err != nil:
		return "", fmt.Errorf("probe: %w", err)
	}

	if current.ClusterID != b.ClusterID {
		return "", fmt.Errorf("cluster_id drift: db=%q desired=%q (stable; refusing rewrite)", current.ClusterID, b.ClusterID)
	}
	if jsonArrayEq(current.Domains, domainsJSON) && current.Issuer == issuer && current.Email == b.Email {
		return "noop", nil
	}
	if err := queries.UpdateBootstrapTLSBundle(ctx, quartermasterdb.UpdateBootstrapTLSBundleParams{
		BundleID: b.ID, Domains: domainsJSON, Issuer: issuer, Email: b.Email,
	}); err != nil {
		return "", fmt.Errorf("update: %w", err)
	}
	return "updated", nil
}

func validateIngressSite(s IngressSite) error {
	if s.ID == "" {
		return errors.New("ingress_site id required")
	}
	if s.ClusterID == "" {
		return fmt.Errorf("ingress_site %q: cluster_id required", s.ID)
	}
	if s.NodeID == "" {
		return fmt.Errorf("ingress_site %q: node_id required", s.ID)
	}
	if len(s.Domains) == 0 {
		return fmt.Errorf("ingress_site %q: domains required", s.ID)
	}
	if s.TLSBundleID == "" {
		return fmt.Errorf("ingress_site %q: tls_bundle_id required", s.ID)
	}
	if s.Kind == "" {
		return fmt.Errorf("ingress_site %q: kind required", s.ID)
	}
	if s.Upstream.Host == "" || s.Upstream.Port == 0 {
		return fmt.Errorf("ingress_site %q: upstream host:port required", s.ID)
	}
	return nil
}

func upsertIngressSite(ctx context.Context, exec DBTX, s IngressSite) (string, error) {
	domainsJSON, err := marshalSortedDomains(s.Domains)
	if err != nil {
		return "", fmt.Errorf("encode domains: %w", err)
	}
	upstream := s.Upstream.Host + ":" + strconv.Itoa(s.Upstream.Port)

	queries := quartermasterdb.New(exec)
	current, err := queries.GetBootstrapIngressSite(ctx, s.ID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if insertErr := queries.InsertBootstrapIngressSite(ctx, quartermasterdb.InsertBootstrapIngressSiteParams{
			SiteID: s.ID, ClusterID: s.ClusterID, NodeID: s.NodeID, Domains: domainsJSON,
			TlsBundleID: s.TLSBundleID, Kind: s.Kind, Upstream: upstream,
		}); insertErr != nil {
			return "", fmt.Errorf("insert: %w", insertErr)
		}
		return "created", nil
	case err != nil:
		return "", fmt.Errorf("probe: %w", err)
	}

	if current.ClusterID != s.ClusterID {
		return "", fmt.Errorf("cluster_id drift: db=%q desired=%q (stable; refusing rewrite)", current.ClusterID, s.ClusterID)
	}
	if current.NodeID != s.NodeID {
		return "", fmt.Errorf("node_id drift: db=%q desired=%q (stable; refusing rewrite)", current.NodeID, s.NodeID)
	}
	if jsonArrayEq(current.Domains, domainsJSON) && current.TlsBundleID == s.TLSBundleID && current.Kind == s.Kind && current.Upstream == upstream {
		return "noop", nil
	}
	if err := queries.UpdateBootstrapIngressSite(ctx, quartermasterdb.UpdateBootstrapIngressSiteParams{
		SiteID: s.ID, Domains: domainsJSON, TlsBundleID: s.TLSBundleID, Kind: s.Kind, Upstream: upstream,
	}); err != nil {
		return "", fmt.Errorf("update: %w", err)
	}
	return "updated", nil
}

// marshalSortedDomains returns a canonical JSON array of domain names so the
// stored representation is order-stable and the noop check survives reorder.
func marshalSortedDomains(domains []string) (string, error) {
	out := make([]string, len(domains))
	copy(out, domains)
	sort.Strings(out)
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// jsonArrayEq compares two JSON-array strings by re-decoding and string-sorting.
// The DB may return whitespace-normalized JSON; canonicalizing avoids spurious
// noop misses.
func jsonArrayEq(a, b string) bool {
	var aa, bb []string
	if err := json.Unmarshal([]byte(a), &aa); err != nil {
		return a == b
	}
	if err := json.Unmarshal([]byte(b), &bb); err != nil {
		return a == b
	}
	sort.Strings(aa)
	sort.Strings(bb)
	if len(aa) != len(bb) {
		return false
	}
	for i := range aa {
		if aa[i] != bb[i] {
			return false
		}
	}
	return true
}
