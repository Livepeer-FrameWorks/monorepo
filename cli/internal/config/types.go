package config

type Endpoints struct {
	BridgeURL             string `yaml:"bridge_url" json:"bridge_url"`
	QuartermasterGRPCAddr string `yaml:"quartermaster_grpc_addr" json:"quartermaster_grpc_addr"`
	CommodoreGRPCAddr     string `yaml:"commodore_grpc_addr" json:"commodore_grpc_addr"`
	FoghornGRPCAddr       string `yaml:"foghorn_grpc_addr" json:"foghorn_grpc_addr"`
	DecklogGRPCAddr       string `yaml:"decklog_grpc_addr" json:"decklog_grpc_addr"`
	PeriscopeGRPCAddr     string `yaml:"periscope_grpc_addr" json:"periscope_grpc_addr"`
	PurserGRPCAddr        string `yaml:"purser_grpc_addr" json:"purser_grpc_addr"`
	SignalmanWSURL        string `yaml:"signalman_ws_url" json:"signalman_ws_url"`
	SignalmanGRPCAddr     string `yaml:"signalman_grpc_addr" json:"signalman_grpc_addr"`
	NavigatorGRPCAddr     string `yaml:"navigator_grpc_addr" json:"navigator_grpc_addr"`

	// TLS configuration for external (non-mesh) connections
	UseTLS        bool   `yaml:"use_tls" json:"use_tls"`                 // Enable TLS for gRPC connections (via Caddy proxy)
	TLSSkipVerify bool   `yaml:"tls_skip_verify" json:"tls_skip_verify"` // Skip TLS certificate verification (dev only!)
	TLSCACert     string `yaml:"tls_ca_cert" json:"tls_ca_cert"`         // Path to CA certificate for custom CAs
}

type Executor struct {
	Type         string `yaml:"type" json:"type"` // local | ssh
	SSHHost      string `yaml:"ssh_host,omitempty" json:"ssh_host,omitempty"`
	SSHUser      string `yaml:"ssh_user,omitempty" json:"ssh_user,omitempty"`
	SSHPort      int    `yaml:"ssh_port,omitempty" json:"ssh_port,omitempty"`
	ProxyCommand string `yaml:"proxy_command,omitempty" json:"proxy_command,omitempty"`
}

type Auth struct {
	ServiceToken string `yaml:"service_token,omitempty" json:"-"`
	JWT          string `yaml:"jwt,omitempty" json:"-"`
}

type Context struct {
	Name      string    `yaml:"name" json:"name"`
	ClusterID string    `yaml:"cluster_id,omitempty" json:"cluster_id,omitempty"`
	Endpoints Endpoints `yaml:"endpoints" json:"endpoints"`
	Executor  Executor  `yaml:"executor" json:"executor"`
	Auth      Auth      `yaml:"auth" json:"-"`
	Persona   Persona   `yaml:"persona,omitempty" json:"persona,omitempty"`
	Gitops    *Gitops   `yaml:"gitops,omitempty" json:"gitops,omitempty"`

	// Remembered state — populated only by the success paths of mutating
	// commands (cluster provision, cluster detect). Read-path resolvers
	// must NOT write to these fields, or context becomes haunted with
	// speculative values from dry-runs and --help paths.
	LastManifestPath string `yaml:"last_manifest_path,omitempty" json:"last_manifest_path,omitempty"`
	SystemTenantID   string `yaml:"system_tenant_id,omitempty" json:"system_tenant_id,omitempty"`
}

// Persona labels a context by operator intent. Shapes setup prompts and
// first-run hints only; commands do not branch on persona.
type Persona string

const (
	PersonaPlatform   Persona = "platform"
	PersonaSelfHosted Persona = "selfhosted"
	PersonaEdge       Persona = "edge"
)

// GitopsSource names the manifest-sourcing strategy for a context.
type GitopsSource string

const (
	GitopsLocal    GitopsSource = "local"
	GitopsGitHub   GitopsSource = "github"
	GitopsManifest GitopsSource = "manifest"
)

// Gitops captures persisted defaults for the manifest resolver.
// LocalPath applies when Source=local; Repo/Ref apply when Source=github;
// ManifestPath applies when Source=manifest (or as an explicit override).
type Gitops struct {
	Source       GitopsSource `yaml:"source" json:"source"`
	LocalPath    string       `yaml:"local_path,omitempty" json:"local_path,omitempty"`
	Repo         string       `yaml:"repo,omitempty" json:"repo,omitempty"`
	Ref          string       `yaml:"ref,omitempty" json:"ref,omitempty"`
	ManifestPath string       `yaml:"manifest_path,omitempty" json:"manifest_path,omitempty"`
	Cluster      string       `yaml:"cluster,omitempty" json:"cluster,omitempty"`
	AgeKeyPath   string       `yaml:"age_key_path,omitempty" json:"age_key_path,omitempty"`
}

type GitHubApp struct {
	AppID          int64  `yaml:"app_id,omitempty"`
	InstallationID int64  `yaml:"installation_id,omitempty"`
	PrivateKeyPath string `yaml:"private_key_path,omitempty"`
	Repo           string `yaml:"repo,omitempty"`
	Ref            string `yaml:"ref,omitempty"`
}

type Config struct {
	Current  string             `yaml:"current"`
	Contexts map[string]Context `yaml:"contexts"`
	GitHub   *GitHubApp         `yaml:"github,omitempty"`
}
