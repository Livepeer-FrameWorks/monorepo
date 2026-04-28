package bootstrap

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ValidationError is a single validation failure tied to a source-field path. The Path
// is dotted/bracketed YAML notation (e.g. `quartermaster.clusters[0].owner_tenant.ref`)
// so operators can find the offending field quickly.
type ValidationError struct {
	Path string
	Msg  string
}

func (e *ValidationError) Error() string { return fmt.Sprintf("%s: %s", e.Path, e.Msg) }

// ValidationErrors collects multiple ValidationError instances. Validate returns this
// type when any check fails so callers can surface every problem at once instead of
// one-by-one.
type ValidationErrors []*ValidationError

func (es ValidationErrors) Error() string {
	if len(es) == 0 {
		return "no errors"
	}
	parts := make([]string, len(es))
	for i, e := range es {
		parts[i] = e.Error()
	}
	return strings.Join(parts, "; ")
}

// AsError returns nil when empty or es when populated, so callers can write
// `return errs.AsError()` cleanly.
func (es ValidationErrors) AsError() error {
	if len(es) == 0 {
		return nil
	}
	return es
}

// Validate exercises a Rendered document against schema and reference-integrity
// invariants. It does not connect to any DB — service bootstrap subcommands run this
// during `--check` mode. Validate is allowed to fail-fast on apiVersion mismatch (no
// other check matters) but otherwise collects all problems before returning.
func (r *Rendered) Validate() error {
	if r == nil {
		return errors.New("nil Rendered")
	}

	var errs ValidationErrors
	r.validateQuartermaster(&errs)
	r.validatePurser(&errs)
	r.validateAccounts(&errs)
	r.validateCrossReferences(&errs)
	return errs.AsError()
}

func (r *Rendered) validateQuartermaster(errs *ValidationErrors) {
	qm := r.Quartermaster

	if qm.SystemTenant != nil {
		if qm.SystemTenant.Alias == "" {
			*errs = append(*errs, &ValidationError{Path: "quartermaster.system_tenant.alias", Msg: "required"})
		} else if qm.SystemTenant.Alias != SystemTenantAlias {
			*errs = append(*errs, &ValidationError{Path: "quartermaster.system_tenant.alias", Msg: fmt.Sprintf("must be %q (the canonical system tenant alias)", SystemTenantAlias)})
		}
		if qm.SystemTenant.Name == "" {
			*errs = append(*errs, &ValidationError{Path: "quartermaster.system_tenant.name", Msg: "required"})
		}
	}

	checkAlias := func(path, alias string) {
		if !validAlias(alias) {
			*errs = append(*errs, &ValidationError{
				Path: path,
				Msg:  fmt.Sprintf("invalid alias %q: must match ^[a-z][a-z0-9-]*$ and be 1-%d chars (operator-visible identifier)", alias, MaxAliasLen),
			})
		}
	}

	seenTenantAliases := map[string]bool{}
	if qm.SystemTenant != nil && qm.SystemTenant.Alias != "" {
		seenTenantAliases[qm.SystemTenant.Alias] = true
	}
	for i, t := range qm.Tenants {
		path := fmt.Sprintf("quartermaster.tenants[%d]", i)
		if t.Alias == "" {
			*errs = append(*errs, &ValidationError{Path: path + ".alias", Msg: "required"})
			continue
		}
		checkAlias(path+".alias", t.Alias)
		if t.Alias == SystemTenantAlias {
			*errs = append(*errs, &ValidationError{Path: path + ".alias", Msg: fmt.Sprintf("alias %q is reserved for the system tenant", SystemTenantAlias)})
		}
		if seenTenantAliases[t.Alias] {
			*errs = append(*errs, &ValidationError{Path: path + ".alias", Msg: fmt.Sprintf("duplicate tenant alias %q", t.Alias)})
		}
		seenTenantAliases[t.Alias] = true
		if t.Name == "" {
			*errs = append(*errs, &ValidationError{Path: path + ".name", Msg: "required"})
		}
	}

	seenClusterIDs := map[string]bool{}
	defaultCount := 0
	for i, c := range qm.Clusters {
		path := fmt.Sprintf("quartermaster.clusters[%d]", i)
		if c.ID == "" {
			*errs = append(*errs, &ValidationError{Path: path + ".id", Msg: "required"})
			continue
		}
		if seenClusterIDs[c.ID] {
			*errs = append(*errs, &ValidationError{Path: path + ".id", Msg: fmt.Sprintf("duplicate cluster id %q", c.ID)})
		}
		seenClusterIDs[c.ID] = true
		if c.OwnerTenant.IsZero() {
			*errs = append(*errs, &ValidationError{Path: path + ".owner_tenant.ref", Msg: "required"})
		} else if !tenantRefSyntaxValid(c.OwnerTenant.Ref) {
			*errs = append(*errs, &ValidationError{Path: path + ".owner_tenant.ref", Msg: fmt.Sprintf("malformed tenant ref %q (expected quartermaster.system_tenant or quartermaster.tenants[<alias>])", c.OwnerTenant.Ref)})
		} else if !tenantRefResolves(c.OwnerTenant.Ref, r) {
			*errs = append(*errs, &ValidationError{Path: path + ".owner_tenant.ref", Msg: fmt.Sprintf("ref %q does not resolve to a tenant in this document", c.OwnerTenant.Ref)})
		}
		if c.Type == "" {
			*errs = append(*errs, &ValidationError{Path: path + ".type", Msg: "required"})
		}
		if c.Mesh.CIDR == "" {
			*errs = append(*errs, &ValidationError{Path: path + ".mesh.cidr", Msg: "required"})
		}
		if c.IsDefault {
			defaultCount++
		}
	}
	if defaultCount > 1 {
		*errs = append(*errs, &ValidationError{
			Path: "quartermaster.clusters",
			Msg:  fmt.Sprintf("multiple clusters marked is_default (%d); exactly one or zero allowed", defaultCount),
		})
	}

	seenNodeIDs := map[string]bool{}
	for i, n := range qm.Nodes {
		path := fmt.Sprintf("quartermaster.nodes[%d]", i)
		if n.ID == "" {
			*errs = append(*errs, &ValidationError{Path: path + ".id", Msg: "required"})
			continue
		}
		if seenNodeIDs[n.ID] {
			*errs = append(*errs, &ValidationError{Path: path + ".id", Msg: fmt.Sprintf("duplicate node id %q", n.ID)})
		}
		seenNodeIDs[n.ID] = true
		if n.ClusterID != "" && !seenClusterIDs[n.ClusterID] {
			*errs = append(*errs, &ValidationError{Path: path + ".cluster_id", Msg: fmt.Sprintf("references unknown cluster %q", n.ClusterID)})
		}
		if n.ExternalIP == "" {
			*errs = append(*errs, &ValidationError{Path: path + ".external_ip", Msg: "required"})
		}
	}

	seenRegistryKeys := map[string]bool{}
	for i, e := range qm.ServiceRegistry {
		path := fmt.Sprintf("quartermaster.service_registry[%d]", i)
		if e.ServiceName == "" {
			*errs = append(*errs, &ValidationError{Path: path + ".service_name", Msg: "required"})
		}
		if e.NodeID == "" {
			*errs = append(*errs, &ValidationError{Path: path + ".node_id", Msg: "required"})
		}
		if e.ServiceName != "" && e.NodeID != "" {
			key := e.ServiceName + "@" + e.NodeID
			if seenRegistryKeys[key] {
				*errs = append(*errs, &ValidationError{Path: path, Msg: fmt.Sprintf("duplicate service_registry entry for (%s, %s)", e.ServiceName, e.NodeID)})
			}
			seenRegistryKeys[key] = true
		}
		// livepeer-gateway entries must carry wallet_address. Without it
		// Purser's deposit monitor silently skips the gateway and tenant
		// deposits never get credited; failing here surfaces the gap at
		// render time instead of as a runtime regression.
		if e.ServiceName == "livepeer-gateway" {
			if wallet := e.Metadata["wallet_address"]; wallet == "" {
				*errs = append(*errs, &ValidationError{
					Path: path + ".metadata.wallet_address",
					Msg:  "required for livepeer-gateway (set eth_acct_addr in services.livepeer-gateway.config, or LIVEPEER_ETH_ACCT_ADDR in the operator's shared env)",
				})
			}
		}
	}

	seenBundleIDs := map[string]bool{}
	for i, b := range qm.Ingress.TLSBundles {
		path := fmt.Sprintf("quartermaster.ingress.tls_bundles[%d]", i)
		if b.ID == "" {
			*errs = append(*errs, &ValidationError{Path: path + ".id", Msg: "required"})
			continue
		}
		if seenBundleIDs[b.ID] {
			*errs = append(*errs, &ValidationError{Path: path + ".id", Msg: fmt.Sprintf("duplicate tls_bundle id %q", b.ID)})
		}
		seenBundleIDs[b.ID] = true
		if b.ClusterID != "" && !seenClusterIDs[b.ClusterID] {
			*errs = append(*errs, &ValidationError{Path: path + ".cluster_id", Msg: fmt.Sprintf("references unknown cluster %q", b.ClusterID)})
		}
		if len(b.Domains) == 0 {
			*errs = append(*errs, &ValidationError{Path: path + ".domains", Msg: "required (non-empty)"})
		}
		if b.Email == "" {
			*errs = append(*errs, &ValidationError{Path: path + ".email", Msg: "required"})
		}
	}

	seenSiteIDs := map[string]bool{}
	for i, s := range qm.Ingress.Sites {
		path := fmt.Sprintf("quartermaster.ingress.sites[%d]", i)
		if s.ID == "" {
			*errs = append(*errs, &ValidationError{Path: path + ".id", Msg: "required"})
			continue
		}
		if seenSiteIDs[s.ID] {
			*errs = append(*errs, &ValidationError{Path: path + ".id", Msg: fmt.Sprintf("duplicate site id %q", s.ID)})
		}
		seenSiteIDs[s.ID] = true
		if s.ClusterID != "" && !seenClusterIDs[s.ClusterID] {
			*errs = append(*errs, &ValidationError{Path: path + ".cluster_id", Msg: fmt.Sprintf("references unknown cluster %q", s.ClusterID)})
		}
		if s.TLSBundleID != "" && !seenBundleIDs[s.TLSBundleID] {
			*errs = append(*errs, &ValidationError{Path: path + ".tls_bundle_id", Msg: fmt.Sprintf("references unknown tls_bundle %q", s.TLSBundleID)})
		}
	}
}

func (r *Rendered) validatePurser(errs *ValidationErrors) {
	seenTierIDs := map[string]bool{}
	for i, t := range r.Purser.BillingTiers {
		path := fmt.Sprintf("purser.billing_tiers[%d]", i)
		if t.ID == "" {
			*errs = append(*errs, &ValidationError{Path: path + ".id", Msg: "required"})
			continue
		}
		if seenTierIDs[t.ID] {
			*errs = append(*errs, &ValidationError{Path: path + ".id", Msg: fmt.Sprintf("duplicate tier id %q in overlay (use a single override entry)", t.ID)})
		}
		seenTierIDs[t.ID] = true
	}

	seenPricingClusterIDs := map[string]bool{}
	for i, p := range r.Purser.ClusterPricing {
		path := fmt.Sprintf("purser.cluster_pricing[%d]", i)
		if p.ClusterID == "" {
			*errs = append(*errs, &ValidationError{Path: path + ".cluster_id", Msg: "required"})
			continue
		}
		if seenPricingClusterIDs[p.ClusterID] {
			*errs = append(*errs, &ValidationError{Path: path + ".cluster_id", Msg: fmt.Sprintf("duplicate cluster_pricing for cluster %q (set override: true on the overlay entry to replace)", p.ClusterID)})
		}
		seenPricingClusterIDs[p.ClusterID] = true
		if p.PricingModel == "" {
			*errs = append(*errs, &ValidationError{Path: path + ".pricing_model", Msg: "required"})
		}
	}

	seenBillingTenantRefs := map[string]bool{}
	for i, b := range r.Purser.CustomerBilling {
		path := fmt.Sprintf("purser.customer_billing[%d]", i)
		if b.Tenant.IsZero() {
			*errs = append(*errs, &ValidationError{Path: path + ".tenant.ref", Msg: "required"})
			continue
		}
		if seenBillingTenantRefs[b.Tenant.Ref] {
			*errs = append(*errs, &ValidationError{Path: path + ".tenant.ref", Msg: fmt.Sprintf("duplicate customer_billing for tenant %q", b.Tenant.Ref)})
		}
		seenBillingTenantRefs[b.Tenant.Ref] = true
		if b.Model == "" {
			*errs = append(*errs, &ValidationError{Path: path + ".model", Msg: "required"})
		} else if b.Model != "prepaid" && b.Model != "postpaid" {
			*errs = append(*errs, &ValidationError{Path: path + ".model", Msg: fmt.Sprintf("must be prepaid or postpaid (got %q)", b.Model)})
		}
		if b.Tier == "" {
			*errs = append(*errs, &ValidationError{Path: path + ".tier", Msg: "required"})
		}
	}
}

func (r *Rendered) validateAccounts(errs *ValidationErrors) {
	for i, a := range r.Accounts {
		path := fmt.Sprintf("accounts[%d]", i)
		switch a.Kind {
		case AccountSystemOperator:
			if !a.Billing.IsNone() {
				*errs = append(*errs, &ValidationError{Path: path + ".billing.model", Msg: "system_operator accounts must have billing.model = none"})
			}
		case AccountCustomer:
			if a.Billing.IsNone() {
				*errs = append(*errs, &ValidationError{Path: path + ".billing.model", Msg: "customer accounts require billing (prepaid|postpaid)"})
			}
		case "":
			*errs = append(*errs, &ValidationError{Path: path + ".kind", Msg: "required"})
		default:
			*errs = append(*errs, &ValidationError{Path: path + ".kind", Msg: fmt.Sprintf("unknown account kind %q", a.Kind)})
		}

		if a.Tenant.Ref == "" {
			*errs = append(*errs, &ValidationError{Path: path + ".tenant.ref", Msg: "required"})
		}

		seenEmails := map[string]bool{}
		for j, u := range a.Users {
			upath := fmt.Sprintf("%s.users[%d]", path, j)
			if u.Email == "" {
				*errs = append(*errs, &ValidationError{Path: upath + ".email", Msg: "required"})
				continue
			}
			if seenEmails[u.Email] {
				*errs = append(*errs, &ValidationError{Path: upath + ".email", Msg: fmt.Sprintf("duplicate email %q within account", u.Email)})
			}
			seenEmails[u.Email] = true
			if u.Role == "" {
				*errs = append(*errs, &ValidationError{Path: upath + ".role", Msg: "required"})
			}
			// Layer-5 contract: a Rendered user must carry plaintext password.
			// First-run reconcile creates the user with this password; subsequent
			// runs no-op against the email lookup. An empty Password here means
			// the renderer skipped resolution or the source omitted password_ref —
			// either way it boots the operator/customer with no usable credential.
			if u.Password == "" {
				*errs = append(*errs, &ValidationError{Path: upath + ".password", Msg: "required (resolved from password_ref); rendered files must carry plaintext for the bootstrap reconciler"})
			}
		}
	}
}

func (r *Rendered) validateCrossReferences(errs *ValidationErrors) {
	clusterIDs := map[string]bool{}
	for _, c := range r.Quartermaster.Clusters {
		clusterIDs[c.ID] = true
	}

	for i, p := range r.Purser.ClusterPricing {
		if p.ClusterID == "" {
			continue
		}
		if !clusterIDs[p.ClusterID] {
			*errs = append(*errs, &ValidationError{
				Path: fmt.Sprintf("purser.cluster_pricing[%d].cluster_id", i),
				Msg:  fmt.Sprintf("references unknown cluster %q (must appear in quartermaster.clusters)", p.ClusterID),
			})
		}
	}

	for i, b := range r.Purser.CustomerBilling {
		if b.Tenant.IsZero() {
			continue
		}
		if !tenantRefResolves(b.Tenant.Ref, r) {
			*errs = append(*errs, &ValidationError{
				Path: fmt.Sprintf("purser.customer_billing[%d].tenant.ref", i),
				Msg:  fmt.Sprintf("ref %q does not resolve to a tenant in this document", b.Tenant.Ref),
			})
		}
	}

	for i, a := range r.Accounts {
		if a.Tenant.IsZero() {
			continue
		}
		if !tenantRefResolves(a.Tenant.Ref, r) {
			*errs = append(*errs, &ValidationError{
				Path: fmt.Sprintf("accounts[%d].tenant.ref", i),
				Msg:  fmt.Sprintf("ref %q does not resolve to a tenant in this document", a.Tenant.Ref),
			})
		}
	}
}

// validAliasRE is the strict alias contract: lowercase letters, digits, and hyphens;
// must start with a letter; 1-MaxAliasLen long. This is the persisted identity key
// for tenants, so we lock it down rather than accepting arbitrary strings.
var validAliasRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

func validAlias(s string) bool {
	return len(s) > 0 && len(s) <= MaxAliasLen && validAliasRE.MatchString(s)
}

// tenantRefRE pins the accepted TenantRef shapes. The alias group reuses the
// alias charset so refs cannot smuggle whitespace, slashes, or stray brackets.
var tenantRefRE = regexp.MustCompile(`^quartermaster\.(?:system_tenant|tenants\[([a-z][a-z0-9-]{0,63})\])$`)

// tenantRefResolves checks a TenantRef against the Rendered document. The two
// accepted forms:
//   - "quartermaster.system_tenant"             — needs SystemTenant present.
//   - "quartermaster.tenants[<alias>]"          — alias must match a tenant entry.
//
// Refs that fail the regex (whitespace, brackets in the alias, wrong group, …)
// short-circuit to false; tenantRefSyntaxValid is a separate check used by
// callers that want to distinguish "malformed" from "unresolved".
func tenantRefResolves(ref string, r *Rendered) bool {
	groups := tenantRefRE.FindStringSubmatch(ref)
	if groups == nil {
		return false
	}
	if ref == "quartermaster.system_tenant" {
		return r.Quartermaster.SystemTenant != nil && r.Quartermaster.SystemTenant.Alias != ""
	}
	alias := groups[1]
	for _, t := range r.Quartermaster.Tenants {
		if t.Alias == alias {
			return true
		}
	}
	return false
}

// tenantRefSyntaxValid reports whether ref's syntax is acceptable, regardless of
// whether the referenced tenant exists in the document. Used to surface
// malformed-ref errors with a clearer message than a bare unresolved-ref.
func tenantRefSyntaxValid(ref string) bool { return tenantRefRE.MatchString(ref) }
