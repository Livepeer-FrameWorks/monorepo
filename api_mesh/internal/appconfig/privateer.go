// Package appconfig holds the typed startup configuration of the Privateer
// binary. scripts/configref generates the operator configuration reference
// from these structs.
package appconfig

import (
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
)

// Privateer is the startup configuration of the Privateer mesh agent. Several
// identity fields fall back to the enrollment state persisted under
// PRIVATEER_DATA_DIR when they are empty, so their required-ness is enforced
// after that state is loaded rather than by this struct.
//
//configref:service privateer cmd=cmd/privateer
type Privateer struct {
	config.HTTPRuntime
	config.Logging

	HTTPPortOverride string `env:"PORT" desc:"TCP port for the HTTP health and metrics listener. Takes precedence over PRIVATEER_PORT when set." introduced:"v0.3.0"`
	HTTPPort         string `env:"PRIVATEER_PORT" default:"@servicedefs.http_port" desc:"TCP port for the HTTP listener that serves health, readiness, and metrics. PORT overrides it." introduced:"v0.3.0"`

	ServiceToken   string `env:"SERVICE_TOKEN" required:"true" secret:"true" desc:"Shared service-to-service bearer token for mesh sync with Quartermaster and certificate calls to Navigator." introduced:"v0.3.0"`
	PrivateKeyFile string `env:"MESH_PRIVATE_KEY_FILE" required:"true" desc:"Path of the WireGuard private key. When the file is missing and MESH_JOIN_TOKEN is set, enrollment generates and writes it." introduced:"v0.3.0"`
	DataDir        string `env:"PRIVATEER_DATA_DIR" desc:"Directory for enrollment state and the last known mesh. Empty uses /var/lib/privateer." introduced:"v0.3.0"`

	QuartermasterGRPCAddr          string `env:"QUARTERMASTER_GRPC_ADDR" desc:"Quartermaster gRPC address for mesh sync. Empty uses the address from persisted enrollment state; startup fails when neither is set." introduced:"v0.3.0"`
	QuartermasterGRPCTLSServerName string `env:"QUARTERMASTER_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Quartermaster connection. Empty uses the canonical internal name." introduced:"v0.3.0"`
	NavigatorGRPCAddr              string `env:"NAVIGATOR_GRPC_ADDR" desc:"Navigator gRPC address for internal and ingress certificate sync. Empty disables certificate sync." introduced:"v0.3.0"`
	NavigatorGRPCTLSServerName     string `env:"NAVIGATOR_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Navigator connection. Empty uses the canonical internal name." introduced:"v0.3.0"`
	GRPCTLSCAPath                  string `env:"GRPC_TLS_CA_PATH" desc:"CA bundle for verifying Quartermaster and Navigator. When the file is missing it is written from the NAVIGATOR_INTERNAL_CA_*_CERT_PEM_B64 values." introduced:"v0.3.0"`
	GRPCAllowInsecure              bool   `env:"GRPC_ALLOW_INSECURE" default:"false" desc:"Allows plaintext gRPC to Quartermaster and Navigator when no CA bundle is configured. Development only." introduced:"v0.3.0"`

	StaticPeersFile     string        `env:"PRIVATEER_STATIC_PEERS_FILE" desc:"Seed peers file applied before the first Quartermaster sync. Empty uses the path from persisted enrollment state; enrollment writes /etc/privateer/static-peers.json when empty." introduced:"v0.3.0"`
	WireguardIP         string        `env:"MESH_WIREGUARD_IP" desc:"Mesh address of this node. Empty uses the address from persisted enrollment state; startup fails when neither is set." introduced:"v0.3.0"`
	WireguardListenPort int           `env:"MESH_LISTEN_PORT" desc:"UDP port of the WireGuard interface. Empty uses the port from persisted enrollment state, then 51820." introduced:"v0.3.0"`
	InterfaceName       string        `env:"MESH_INTERFACE" desc:"WireGuard interface name. Empty uses wg0." introduced:"v0.3.0"`
	NodeType            string        `env:"MESH_NODE_TYPE" desc:"Node type reported to Quartermaster and sent at enrollment. Empty uses core." introduced:"v0.3.0"`
	NodeName            string        `env:"MESH_NODE_NAME" desc:"Node name reported to Quartermaster and sent at enrollment. Empty uses the host name." introduced:"v0.3.0"`
	NodeID              string        `env:"NODE_ID" desc:"Node identity for mesh sync and certificate issuance. Empty uses persisted enrollment state, then a generated ID stored on disk." introduced:"v0.3.0"`
	ClusterID           string        `env:"CLUSTER_ID" desc:"Cluster this node belongs to; also the target cluster requested at enrollment. Empty uses persisted enrollment state; startup fails when neither is set." introduced:"v0.3.0"`
	ExternalIP          string        `env:"MESH_EXTERNAL_IP" desc:"Public address reported to Quartermaster and sent at enrollment." introduced:"v0.3.0"`
	InternalIP          string        `env:"MESH_INTERNAL_IP" desc:"Private network address reported to Quartermaster and sent at enrollment." introduced:"v0.3.0"`
	SyncInterval        time.Duration `env:"PRIVATEER_SYNC_INTERVAL" default:"30s" desc:"Interval between mesh sync calls to Quartermaster." introduced:"v0.3.0"`
	SyncTimeout         time.Duration `env:"PRIVATEER_SYNC_TIMEOUT" default:"10s" desc:"Timeout for one mesh sync call to Quartermaster." introduced:"v0.3.0"`

	DNSPort     int      `env:"DNS_PORT" default:"53" desc:"Port of the internal DNS server, which listens on 127.0.0.1 over UDP and TCP." introduced:"v0.3.0"`
	UpstreamDNS []string `env:"UPSTREAM_DNS" desc:"Comma-separated resolvers for names outside .internal. Entries without a port use 53. Empty answers those queries with REFUSED." introduced:"v0.3.0"`

	CertIssuanceToken            string        `env:"CERT_ISSUANCE_TOKEN" secret:"true" desc:"Initial token for requesting internal service certificates from Navigator. Privateer mints replacements when it expires or is rejected." introduced:"v0.3.0"`
	PKIDir                       string        `env:"GRPC_TLS_PKI_DIR" default:"/etc/frameworks/pki" desc:"Directory where synced internal service certificates and keys are written." introduced:"v0.3.0"`
	ExpectedInternalGRPCServices []string      `env:"EXPECTED_INTERNAL_GRPC_SERVICES" desc:"Comma-separated service types that need internal certificates on this node, in addition to those registered in Quartermaster." introduced:"v0.3.0"`
	CertSyncInterval             time.Duration `env:"PRIVATEER_CERT_SYNC_INTERVAL" default:"5m" desc:"Interval between internal and ingress certificate sync passes." introduced:"v0.3.0"`

	InternalCARootCertPEMB64         string `env:"NAVIGATOR_INTERNAL_CA_ROOT_CERT_PEM_B64" desc:"Base64-wrapped internal root certificate PEM written into GRPC_TLS_CA_PATH when that file is missing." introduced:"v0.3.0"`
	InternalCAIntermediateCertPEMB64 string `env:"NAVIGATOR_INTERNAL_CA_INTERMEDIATE_CERT_PEM_B64" desc:"Base64-wrapped internal intermediate certificate PEM written into GRPC_TLS_CA_PATH with the root certificate." introduced:"v0.3.0"`

	JoinToken           string `env:"MESH_JOIN_TOKEN" secret:"true" desc:"One-time enrollment token. Used only when MESH_PRIVATE_KEY_FILE does not exist yet." introduced:"v0.3.0"`
	BridgeBootstrapAddr string `env:"BRIDGE_BOOTSTRAP_ADDR" desc:"Bridge address for enrollment. Required when MESH_JOIN_TOKEN is used; an address without a scheme uses https." introduced:"v0.3.0"`
	BootstrapInsecure   bool   `env:"FRAMEWORKS_BOOTSTRAP_INSECURE" default:"false" desc:"Skips TLS verification of the Bridge enrollment request. Development and test only." introduced:"v0.3.0"`
}

// ListenHTTPPort returns the HTTP listener port: PORT when set, otherwise
// PRIVATEER_PORT.
func (c *Privateer) ListenHTTPPort() string {
	if c.HTTPPortOverride != "" {
		return c.HTTPPortOverride
	}
	return c.HTTPPort
}
