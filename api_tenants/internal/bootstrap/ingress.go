package bootstrap

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
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

	const probeSQL = `
		SELECT cluster_id, COALESCE(domains::text, '[]'), issuer, email
		FROM quartermaster.tls_bundles
		WHERE bundle_id = $1`
	var curCluster, curDomains, curIssuer, curEmail string
	err = exec.QueryRowContext(ctx, probeSQL, b.ID).Scan(&curCluster, &curDomains, &curIssuer, &curEmail)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		const insertSQL = `
			INSERT INTO quartermaster.tls_bundles (bundle_id, cluster_id, domains, issuer, email, updated_at)
			VALUES ($1, $2, $3::jsonb, $4, $5, NOW())`
		if _, insertErr := exec.ExecContext(ctx, insertSQL, b.ID, b.ClusterID, domainsJSON, issuer, b.Email); insertErr != nil {
			return "", fmt.Errorf("insert: %w", insertErr)
		}
		return "created", nil
	case err != nil:
		return "", fmt.Errorf("probe: %w", err)
	}

	if curCluster != b.ClusterID {
		return "", fmt.Errorf("cluster_id drift: db=%q desired=%q (stable; refusing rewrite)", curCluster, b.ClusterID)
	}
	if jsonArrayEq(curDomains, domainsJSON) && curIssuer == issuer && curEmail == b.Email {
		return "noop", nil
	}
	const updateSQL = `
		UPDATE quartermaster.tls_bundles
		SET domains = $2::jsonb, issuer = $3, email = $4, updated_at = NOW()
		WHERE bundle_id = $1`
	if _, err := exec.ExecContext(ctx, updateSQL, b.ID, domainsJSON, issuer, b.Email); err != nil {
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

	const probeSQL = `
		SELECT cluster_id, node_id, COALESCE(domains::text, '[]'), tls_bundle_id, kind, upstream
		FROM quartermaster.ingress_sites
		WHERE site_id = $1`
	var curCluster, curNode, curDomains, curBundle, curKind, curUpstream string
	err = exec.QueryRowContext(ctx, probeSQL, s.ID).Scan(&curCluster, &curNode, &curDomains, &curBundle, &curKind, &curUpstream)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		const insertSQL = `
			INSERT INTO quartermaster.ingress_sites (site_id, cluster_id, node_id, domains, tls_bundle_id, kind, upstream, updated_at)
			VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7, NOW())`
		if _, insertErr := exec.ExecContext(ctx, insertSQL, s.ID, s.ClusterID, s.NodeID, domainsJSON, s.TLSBundleID, s.Kind, upstream); insertErr != nil {
			return "", fmt.Errorf("insert: %w", insertErr)
		}
		return "created", nil
	case err != nil:
		return "", fmt.Errorf("probe: %w", err)
	}

	if curCluster != s.ClusterID {
		return "", fmt.Errorf("cluster_id drift: db=%q desired=%q (stable; refusing rewrite)", curCluster, s.ClusterID)
	}
	if curNode != s.NodeID {
		return "", fmt.Errorf("node_id drift: db=%q desired=%q (stable; refusing rewrite)", curNode, s.NodeID)
	}
	if jsonArrayEq(curDomains, domainsJSON) && curBundle == s.TLSBundleID && curKind == s.Kind && curUpstream == upstream {
		return "noop", nil
	}
	const updateSQL = `
		UPDATE quartermaster.ingress_sites
		SET domains = $2::jsonb, tls_bundle_id = $3, kind = $4, upstream = $5, updated_at = NOW()
		WHERE site_id = $1`
	if _, err := exec.ExecContext(ctx, updateSQL, s.ID, domainsJSON, s.TLSBundleID, s.Kind, upstream); err != nil {
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
