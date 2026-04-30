// Package bootstrap defines the desired-state schema for cluster bootstrap. It carries
// three distinct top-level types (Derived, Overlay, Rendered) so the type system enforces
// the layer boundary: Derived is what the manifest produces, Overlay is the GitOps
// addition, Rendered is the resolved-secrets output handed to service bootstrap
// subcommands.
//
// See docs/architecture/bootstrap-desired-state.md for the canonical schema, ownership
// boundaries, and merge precedence.
package bootstrap

// The rendered file carries no schema-version field. The Purser/Commodore/QM
// binary that consumes a rendered file ships with the YAML decoder using
// KnownFields(true), so any field it doesn't recognize fails parse. The binary
// version is effectively the schema version. If a real on-the-wire break shows
// up later, we add a `schema:` integer at that moment — not preemptively.

// SecretRef is a deferred secret value carried in Derived and Overlay. The CLI resolves
// SecretRef into plaintext at render time; Rendered files carry plaintext under mode
// 0600 and are removed after the bootstrap subcommand consumes them.
//
// Exactly one of SOPS+Key, Env, File, or Flag must be set. Validation fails any other
// shape.
type SecretRef struct {
	// SOPS path (relative to the gitops repo root or absolute) to a SOPS-encrypted
	// env or yaml file. Key is the field within the decrypted file.
	SOPS string `yaml:"sops,omitempty" json:"sops,omitempty"`
	Key  string `yaml:"key,omitempty"  json:"key,omitempty"`

	// Env names an environment variable read at render time.
	Env string `yaml:"env,omitempty" json:"env,omitempty"`

	// File is a local-filesystem path read at render time. Mode 0400 expected.
	File string `yaml:"file,omitempty" json:"file,omitempty"`

	// Flag names a CLI flag whose value is read at render time (e.g.
	// "bootstrap-admin-password").
	Flag string `yaml:"flag,omitempty" json:"flag,omitempty"`
}

// IsZero reports whether the ref is unset.
func (s SecretRef) IsZero() bool {
	return s == SecretRef{}
}

// === Quartermaster section types (shared shape across layers) ===

// SystemTenantAlias is the canonical alias for the platform/system tenant. It is
// embedded in every Derived/Rendered file's Quartermaster.SystemTenant.Alias.
const SystemTenantAlias = "frameworks"

// MaxAliasLen bounds the alias length so persisted alias→UUID storage and ref
// strings stay readable. Aliases are operator-visible identifiers, not free text.
const MaxAliasLen = 64

// Tenant is a Quartermaster-owned tenant identity carried in the bootstrap file. The
// rendered file does NOT carry the DB UUID — Quartermaster generates UUIDs at apply
// time. Tenants are referenced through the file by Alias, which is the stable key.
//
// Consumer obligation: `quartermaster bootstrap` must persist the alias → UUID
// mapping in QM-owned storage (e.g. `quartermaster.bootstrap_tenant_aliases`) so
// re-runs find the same tenant by alias. Without that, bootstrap is non-idempotent.
type Tenant struct {
	Alias          string `yaml:"alias"`
	Name           string `yaml:"name"`
	DeploymentTier string `yaml:"deployment_tier,omitempty"`
	PrimaryColor   string `yaml:"primary_color,omitempty"`
	SecondaryColor string `yaml:"secondary_color,omitempty"`
}

// Cluster is a Quartermaster-owned infrastructure cluster. Stable keys: ID,
// OwnerTenant (as a ref), Mesh.CIDR (when the cluster already exists).
type Cluster struct {
	ID                 string      `yaml:"id"`
	Name               string      `yaml:"name"`
	Type               string      `yaml:"type"` // "central" | "edge"
	Region             string      `yaml:"region,omitempty"`
	OwnerTenant        TenantRef   `yaml:"owner_tenant"`
	IsDefault          bool        `yaml:"is_default,omitempty"`
	IsPlatformOfficial bool        `yaml:"is_platform_official,omitempty"`
	BaseURL            string      `yaml:"base_url,omitempty"`
	Mesh               ClusterMesh `yaml:"mesh"`

	// Override = true on an Overlay item replaces the manifest-derived entry with
	// the same ID. Ignored on Derived and Rendered.
	Override bool `yaml:"override,omitempty"`
}

type ClusterMesh struct {
	CIDR       string `yaml:"cidr"`
	ListenPort int    `yaml:"listen_port"`
}

// Node is a Quartermaster-owned infrastructure node. Stable keys: ID, ClusterID,
// ExternalIP, WireGuard.IP.
type Node struct {
	ID         string        `yaml:"id"`
	ClusterID  string        `yaml:"cluster_id"`
	Type       string        `yaml:"type"` // "core" | "edge"
	ExternalIP string        `yaml:"external_ip"`
	WireGuard  NodeWireGuard `yaml:"wireguard"`
}

type NodeWireGuard struct {
	IP        string `yaml:"ip"`
	PublicKey string `yaml:"public_key"`
	Port      int    `yaml:"port,omitempty"`
}

// Mesh is the per-cluster mesh configuration (separate from per-node WireGuard
// identities). Stable on CIDR.
type Mesh struct {
	CIDR            string `yaml:"cidr"`
	ListenPort      int    `yaml:"listen_port,omitempty"`
	ManageHostsFile bool   `yaml:"manage_hosts_file,omitempty"`
}

// TLSBundle is a Quartermaster-owned ingress TLS bundle. Stable key: ID.
type TLSBundle struct {
	ID        string   `yaml:"id"`
	ClusterID string   `yaml:"cluster_id"`
	Domains   []string `yaml:"domains"`
	Issuer    string   `yaml:"issuer,omitempty"`
	Email     string   `yaml:"email,omitempty"`
}

// IngressSite is a Quartermaster-owned ingress site. Stable key: ID.
type IngressSite struct {
	ID          string          `yaml:"id"`
	ClusterID   string          `yaml:"cluster_id"`
	NodeID      string          `yaml:"node_id"`
	Domains     []string        `yaml:"domains"`
	TLSBundleID string          `yaml:"tls_bundle_id"`
	Kind        string          `yaml:"kind"` // "http" | "tcp" | …
	Upstream    IngressUpstream `yaml:"upstream"`
}

type IngressUpstream struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
}

// IngressSection groups TLS bundles and sites under quartermaster.ingress.
type IngressSection struct {
	TLSBundles []TLSBundle   `yaml:"tls_bundles,omitempty"`
	Sites      []IngressSite `yaml:"sites,omitempty"`
}

// ServiceRegistryEntry is a Quartermaster-owned service registry record. Stable key:
// (ServiceName, NodeID).
//
// Metadata is always rendered authoritatively from the manifest (or per-service
// derivation, see render.go's deriveServiceMetadata). The bootstrap reconciler
// writes it as-is and must not enrich it from on-host state. If a service needs
// values that are only available at apply time, model them as SecretRef-backed
// fields in this schema, not as silent runtime reads inside the reconciler.
type ServiceRegistryEntry struct {
	ServiceName    string            `yaml:"service_name"`
	Type           string            `yaml:"type"`
	Protocol       string            `yaml:"protocol,omitempty"` // "http" | "grpc" | "tcp"
	ClusterID      string            `yaml:"cluster_id"`
	NodeID         string            `yaml:"node_id"`
	Port           int               `yaml:"port"`
	HealthEndpoint string            `yaml:"health_endpoint,omitempty"`
	Metadata       map[string]string `yaml:"metadata,omitempty"`
}

// SystemTenantClusterAccess controls the post-cluster reconcile that subscribes the
// system tenant to default and platform-official clusters. Reconciled inside
// quartermaster bootstrap after clusters exist.
type SystemTenantClusterAccess struct {
	DefaultClusters          bool `yaml:"default_clusters"`
	PlatformOfficialClusters bool `yaml:"platform_official_clusters"`
}

// QuartermasterSection is the shape of the quartermaster: top-level key. Identical
// shape across Derived, Overlay, and Rendered — Quartermaster carries no secret values
// in the bootstrap path.
type QuartermasterSection struct {
	SystemTenant              *Tenant                    `yaml:"system_tenant,omitempty"`
	Tenants                   []Tenant                   `yaml:"tenants,omitempty"`
	Clusters                  []Cluster                  `yaml:"clusters,omitempty"`
	Nodes                     []Node                     `yaml:"nodes,omitempty"`
	Mesh                      *Mesh                      `yaml:"mesh,omitempty"`
	Ingress                   IngressSection             `yaml:"ingress,omitempty"`
	ServiceRegistry           []ServiceRegistryEntry     `yaml:"service_registry,omitempty"`
	SystemTenantClusterAccess *SystemTenantClusterAccess `yaml:"system_tenant_cluster_access,omitempty"`
}

// === Purser section types ===

// ClusterPricing is a Purser-owned per-cluster pricing record. Stable key: ClusterID.
// Override = true on an overlay entry replaces the derived/earlier entry with the same
// ClusterID. Without Override, a same-ClusterID overlay entry is a configuration error.
type ClusterPricing struct {
	ClusterID         string         `yaml:"cluster_id"`
	PricingModel      string         `yaml:"pricing_model"`
	RequiredTierLevel *int32         `yaml:"required_tier_level,omitempty"`
	AllowFreeTier     *bool          `yaml:"allow_free_tier,omitempty"`
	BasePrice         string         `yaml:"base_price,omitempty"`
	Currency          string         `yaml:"currency,omitempty"`
	MeteredRates      map[string]any `yaml:"metered_rates,omitempty"`
	DefaultQuotas     map[string]any `yaml:"default_quotas,omitempty"`
	Override          bool           `yaml:"override,omitempty"`
}

// BillingTier is a Purser-owned tier definition. The Purser binary embeds the
// canonical catalog at compile time; this slot is the overlay surface for adding
// new tiers or overriding fields on built-in tiers. Stable key: ID.
type BillingTier struct {
	ID               string   `yaml:"id"`
	DisplayName      string   `yaml:"display_name,omitempty"`
	TierLevel        int32    `yaml:"tier_level,omitempty"`
	BasePriceMonthly string   `yaml:"base_price_monthly,omitempty"`
	Currency         string   `yaml:"currency,omitempty"`
	Features         []string `yaml:"features,omitempty"`
	// Entitlements are non-billing grants (e.g. recording_retention_days).
	Entitlements map[string]any `yaml:"entitlements,omitempty"`
	// PricingRules wholly replace the embedded tier's rules when present on an
	// overlay entry — there is no per-rule merge.
	PricingRules []OverlayPricingRule `yaml:"pricing_rules,omitempty"`
	// Override = true on an overlay entry merges field-by-field over the embedded
	// catalog's tier with the same ID. Without Override, an ID collision with the
	// embedded catalog is a configuration error.
	Override bool `yaml:"override,omitempty"`
}

// OverlayPricingRule is the operator-overlay representation of a pricing rule.
// Mirrors api_billing/internal/bootstrap.CatalogPricingRule; kept here as a
// separate type so the CLI doesn't depend on the Purser package.
type OverlayPricingRule struct {
	Meter            string         `yaml:"meter"`
	Model            string         `yaml:"model"`
	Currency         string         `yaml:"currency,omitempty"`
	IncludedQuantity float64        `yaml:"included_quantity,omitempty"`
	UnitPrice        string         `yaml:"unit_price"`
	Config           map[string]any `yaml:"config,omitempty"`
}

// CustomerBilling is a Purser-owned per-customer-tenant billing/subscription record.
// Tenant references the QM tenant by alias; Purser bootstrap resolves the alias to a
// UUID via QM's persisted alias mapping at apply time. Stable key: Tenant ref.
type CustomerBilling struct {
	Tenant        TenantRef `yaml:"tenant"`
	Model         string    `yaml:"model"` // "prepaid" | "postpaid"
	Tier          string    `yaml:"tier"`
	ClusterAccess string    `yaml:"cluster_access,omitempty"` // "derived"
}

// PurserSection is the shape of the purser: top-level key. Identical across layers —
// Purser bootstrap carries no secret values.
//
// BillingTiers carries the layer-4 (overlay) catalog entries: tiers added by GitOps,
// or field-level overrides against the binary-embedded catalog. The embedded catalog
// (layer 2, owned by Purser) is the baseline; this slot is the operator's surface for
// tweaking it without modifying the binary.
type PurserSection struct {
	BillingTiers    []BillingTier     `yaml:"billing_tiers,omitempty"`
	ClusterPricing  []ClusterPricing  `yaml:"cluster_pricing,omitempty"`
	CustomerBilling []CustomerBilling `yaml:"customer_billing,omitempty"`
}

// === Account types — these differ by layer because they carry secrets ===

// AccountKind distinguishes operator/system accounts (skip Purser by design) from
// customer accounts (full QM+Purser+Commodore coordination).
type AccountKind string

const (
	AccountSystemOperator AccountKind = "system_operator"
	AccountCustomer       AccountKind = "customer"
)

// TenantRef is a path-style reference into the same desired-state document. The
// rendered file never carries DB UUIDs; service bootstrap subcommands resolve refs to
// concrete tenant UUIDs via QM's alias-mapping table at apply time.
//
// Forms:
//   - "quartermaster.system_tenant"             — refers to the system tenant.
//   - "quartermaster.tenants[<alias>]"          — refers to a customer tenant by alias.
type TenantRef struct {
	Ref string `yaml:"ref"`
}

// IsSystem reports whether the ref points at the system tenant.
func (r TenantRef) IsSystem() bool { return r.Ref == "quartermaster.system_tenant" }

// IsZero reports whether the ref is unset.
func (r TenantRef) IsZero() bool { return r.Ref == "" }

// TenantRefSystem returns the canonical ref for the system tenant.
func TenantRefSystem() TenantRef { return TenantRef{Ref: "quartermaster.system_tenant"} }

// TenantRefAlias returns the canonical ref for a customer tenant by alias.
func TenantRefAlias(alias string) TenantRef {
	return TenantRef{Ref: "quartermaster.tenants[" + alias + "]"}
}

// AccountUserCommon is the secret-free portion of a user entry, shared across layers.
type AccountUserCommon struct {
	Email            string `yaml:"email"`
	Role             string `yaml:"role"`
	FirstName        string `yaml:"first_name,omitempty"`
	LastName         string `yaml:"last_name,omitempty"`
	ResetCredentials bool   `yaml:"reset_credentials,omitempty"`
}

// AccountUserDerived is a user entry as it appears in Derived/Overlay layers, with the
// password as a SecretRef.
type AccountUserDerived struct {
	AccountUserCommon `yaml:",inline"`
	PasswordRef       SecretRef `yaml:"password_ref,omitempty"`
}

// AccountUserRendered is a user entry as it appears in Rendered, with the password
// resolved to plaintext. Rendered files carry plaintext under mode 0600.
type AccountUserRendered struct {
	AccountUserCommon `yaml:",inline"`
	Password          string `yaml:"password,omitempty"`
}

// AccountBilling carries the customer-billing declaration for an account. For
// system_operator accounts, Mode is "none".
type AccountBilling struct {
	Mode          string `yaml:"model,omitempty"` // "none" | "prepaid" | "postpaid"
	Tier          string `yaml:"tier,omitempty"`
	ClusterAccess string `yaml:"cluster_access,omitempty"` // "derived"
}

// IsNone reports whether this account explicitly skips Purser (system_operator).
func (b AccountBilling) IsNone() bool { return b.Mode == "" || b.Mode == "none" }

// AccountDerived is an account entry in the Derived/Overlay layers.
type AccountDerived struct {
	Kind    AccountKind          `yaml:"kind"`
	Tenant  TenantRef            `yaml:"tenant"`
	Users   []AccountUserDerived `yaml:"users,omitempty"`
	Billing AccountBilling       `yaml:"billing,omitempty"`
}

// AccountRendered is an account entry in the Rendered output, with all secrets resolved.
type AccountRendered struct {
	Kind    AccountKind           `yaml:"kind"`
	Tenant  TenantRef             `yaml:"tenant"`
	Users   []AccountUserRendered `yaml:"users,omitempty"`
	Billing AccountBilling        `yaml:"billing,omitempty"`
}

// === Top-level layer types ===

// Derived is the bootstrap state computed by the CLI from the cluster manifest. Secret
// values are kept as SecretRef.
type Derived struct {
	Quartermaster QuartermasterSection `yaml:"quartermaster,omitempty"`
	Purser        PurserSection        `yaml:"purser,omitempty"`
	Accounts      []AccountDerived     `yaml:"accounts,omitempty"`
}

// Overlay is the hand-authored GitOps overlay layered onto the manifest-derived state.
// Secret values stay as SecretRef. Items marked Override = true replace derived entries
// with the same stable key; otherwise overlay items are additive.
type Overlay struct {
	Quartermaster QuartermasterSection `yaml:"quartermaster,omitempty"`
	Purser        PurserSection        `yaml:"purser,omitempty"`
	Accounts      []AccountDerived     `yaml:"accounts,omitempty"`
}

// Rendered is the final desired-state document handed to a service bootstrap subcommand.
// All secrets are resolved to plaintext. Render produces this; Validate exercises it;
// service bootstrap subcommands consume it.
type Rendered struct {
	Quartermaster QuartermasterSection `yaml:"quartermaster,omitempty"`
	Purser        PurserSection        `yaml:"purser,omitempty"`
	Accounts      []AccountRendered    `yaml:"accounts,omitempty"`
}
