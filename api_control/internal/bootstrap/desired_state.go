// Package bootstrap holds Commodore's reconcilers for the bootstrap-desired-state
// schema (see docs/architecture/bootstrap-desired-state.md). The `commodore
// bootstrap` subcommand consumes only the accounts: top-level section; tenant
// identity, billing, cluster, and ingress slices are owned by Quartermaster and
// Purser respectively.
package bootstrap

// DesiredState is the slice of the rendered bootstrap file Commodore consumes.
// Other top-level sections (quartermaster, purser, …) are ignored at decode
// time so a single rendered file can be served to all three services.
type DesiredState struct {
	Accounts  []Account        `yaml:"accounts,omitempty"`
	Commodore CommodoreSection `yaml:"commodore,omitempty"`
}

// SystemTenantAlias is the canonical alias for the platform/system tenant.
// Kept in sync with cli/pkg/bootstrap.SystemTenantAlias — the cross-service
// contract is the alias literal, not the Go constant.
const SystemTenantAlias = "frameworks"

// CommodoreSection mirrors cli/pkg/bootstrap.CommodoreRenderedSection for
// operator-owned Commodore resources reconciled by commodore bootstrap.
type CommodoreSection struct {
	PullStreams       []PullStream       `yaml:"pull_streams,omitempty"`
	MistNativeStreams []MistNativeStream `yaml:"mist_native_streams,omitempty"`
}

// PullStream mirrors cli/pkg/bootstrap.PullStreamRendered's wire format.
// Stable key: PlaybackID. SourceURI is plaintext; the commodore bootstrap
// subcommand encrypts via the same FieldEncryptor used by runtime CRUD before
// inserting into commodore.stream_pull_sources.
type PullStream struct {
	PlaybackID        string    `yaml:"playback_id"`
	OwnerTenant       TenantRef `yaml:"owner_tenant"`
	Title             string    `yaml:"title"`
	Description       string    `yaml:"description,omitempty"`
	SourceURI         string    `yaml:"source_uri"`
	Enabled           bool      `yaml:"enabled"`
	AllowedClusterIDs []string  `yaml:"allowed_cluster_ids,omitempty"`
}

// MistNativeStream mirrors cli/pkg/bootstrap.MistNativeStreamRendered's wire
// format. Stable key: PlaybackID. The source is a literal Mist input string
// (e.g. ts-exec:ffmpeg ...) — the CLI render layer is responsible for
// source_kind validation. ProcessPolicy is the per-stream MistServer process
// config; the reconciler serializes it to JSON and writes it to
// commodore.stream_processing_config (NOT to commodore.streams) so the
// existing process-policy authority stays in one place.
type MistNativeStream struct {
	PlaybackID         string                  `yaml:"playback_id"`
	OwnerTenant        TenantRef               `yaml:"owner_tenant"`
	Title              string                  `yaml:"title"`
	Description        string                  `yaml:"description,omitempty"`
	Source             string                  `yaml:"source"`
	SourceKind         string                  `yaml:"source_kind"`
	AlwaysOn           bool                    `yaml:"always_on"`
	IsRecordingEnabled bool                    `yaml:"is_recording_enabled,omitempty"`
	ProcessPolicy      any                     `yaml:"process_policy,omitempty"`
	PlacementCount     int                     `yaml:"placement_count,omitempty"`
	AllowedClusterIDs  []string                `yaml:"allowed_cluster_ids,omitempty"`
	LocalAssets        []MistNativeStreamAsset `yaml:"local_assets,omitempty"`
}

// MistNativeStreamAsset declares one expected on-disk file. Bootstrap stores
// these in stream_mist_sources.local_asset_paths for audit; Ansible places
// the actual files on the edge nodes.
type MistNativeStreamAsset struct {
	Path   string `yaml:"path"`
	Sha256 string `yaml:"sha256,omitempty"`
	Note   string `yaml:"note,omitempty"`
}

// Account mirrors cli/pkg/bootstrap.AccountRendered's wire format. Field shapes
// are duplicated rather than imported across modules so api_control stays free
// of a cli/* dependency; the YAML schema is the cross-service contract.
//
// Kind distinguishes the two account flavors:
//
//   - system_operator: platform/operator account; Tenant references the QM system
//     tenant. Commodore creates the user(s); Purser is skipped (Billing.Mode = "none").
//   - customer: end-customer account; Commodore creates the user(s) under a
//     customer tenant resolved via QM's alias→UUID mapping.
type Account struct {
	Kind    AccountKind    `yaml:"kind"`
	Tenant  TenantRef      `yaml:"tenant"`
	Users   []AccountUser  `yaml:"users,omitempty"`
	Billing AccountBilling `yaml:"billing,omitempty"`
}

type AccountKind string

const (
	AccountSystemOperator AccountKind = "system_operator"
	AccountCustomer       AccountKind = "customer"
)

// AccountUser is a single user entry under an Account. Password is plaintext —
// the rendered file is mode 0600 and is removed after the bootstrap subcommand
// consumes it (see Ansible role; bootstrap subcommands are run as one-shots).
type AccountUser struct {
	Email            string `yaml:"email"`
	Role             string `yaml:"role"`
	FirstName        string `yaml:"first_name,omitempty"`
	LastName         string `yaml:"last_name,omitempty"`
	Password         string `yaml:"password,omitempty"`
	ResetCredentials bool   `yaml:"reset_credentials,omitempty"`
}

// AccountBilling carries the customer-billing declaration. For system_operator
// accounts, Mode is "none" (or empty) and Commodore ignores the rest. Purser
// owns the customer-billing reconcile path; Commodore reads this only to
// distinguish operator/customer behavior.
type AccountBilling struct {
	Mode          string `yaml:"model,omitempty"` // "none" | "prepaid" | "postpaid"
	Tier          string `yaml:"tier,omitempty"`
	ClusterAccess string `yaml:"cluster_access,omitempty"`
}

// TenantRef mirrors cli/pkg/bootstrap.TenantRef.
type TenantRef struct {
	Ref string `yaml:"ref"`
}

// Result reports per-row reconciler outcomes so callers can assert idempotency.
type Result struct {
	Created []string
	Updated []string
	Noop    []string
	Deleted []string
}

func (r Result) Total() int {
	return len(r.Created) + len(r.Updated) + len(r.Noop) + len(r.Deleted)
}
