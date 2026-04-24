package handlers

import (
	"net/http"
	"strings"

	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	qmclient "frameworks/pkg/clients/quartermaster"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// InfrastructureBootstrapHandler is the public HTTP endpoint new
// infrastructure nodes POST to in order to enroll into the mesh. Bridge is
// the only mesh-external surface we expose for Quartermaster — keeping QM's
// gRPC listener mesh-only preserves the invariant that the control plane
// sits behind the mesh once the cluster is alive.
//
// Authorization is the bootstrap token in the request body, validated by
// Quartermaster via its normal BootstrapInfrastructureNode flow. Bridge is a
// pure pass-through; it does not itself inspect the token.
type InfrastructureBootstrapHandler struct {
	qm     *qmclient.GRPCClient
	logger logging.Logger
}

func NewInfrastructureBootstrapHandler(qm *qmclient.GRPCClient, logger logging.Logger) *InfrastructureBootstrapHandler {
	return &InfrastructureBootstrapHandler{qm: qm, logger: logger}
}

type infraBootstrapRequest struct {
	Token              string `json:"token"`
	NodeType           string `json:"node_type"`
	NodeID             string `json:"node_id,omitempty"`
	Hostname           string `json:"hostname,omitempty"`
	ExternalIP         string `json:"external_ip,omitempty"`
	InternalIP         string `json:"internal_ip,omitempty"`
	TargetClusterID    string `json:"target_cluster_id,omitempty"`
	WireguardIP        string `json:"wireguard_ip,omitempty"`
	WireguardPublicKey string `json:"wireguard_public_key"`
	WireguardPort      int32  `json:"wireguard_port,omitempty"`
}

type infraBootstrapPeer struct {
	NodeName   string   `json:"node_name"`
	PublicKey  string   `json:"public_key"`
	Endpoint   string   `json:"endpoint"`
	AllowedIPs []string `json:"allowed_ips"`
	KeepAlive  int32    `json:"keep_alive"`
}

type infraBootstrapResponse struct {
	NodeID                string               `json:"node_id"`
	TenantID              *string              `json:"tenant_id,omitempty"`
	ClusterID             string               `json:"cluster_id"`
	WireguardIP           string               `json:"wireguard_ip"`
	WireguardPort         int32                `json:"wireguard_port"`
	MeshCIDR              string               `json:"mesh_cidr"`
	QuartermasterGRPCAddr string               `json:"quartermaster_grpc_addr"`
	SeedPeers             []infraBootstrapPeer `json:"seed_peers"`
	SeedServiceEndpoints  map[string][]string  `json:"seed_service_endpoints"`
	CABundle              string               `json:"ca_bundle,omitempty"`
}

func (h *InfrastructureBootstrapHandler) Handle(c *gin.Context) {
	var body infraBootstrapRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON body: " + err.Error()})
		return
	}
	if strings.TrimSpace(body.Token) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "token is required"})
		return
	}
	if strings.TrimSpace(body.WireguardPublicKey) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "wireguard_public_key is required — generate the keypair locally and send only the public half"})
		return
	}
	if strings.TrimSpace(body.NodeType) == "" {
		body.NodeType = "core"
	}

	req := &pb.BootstrapInfrastructureNodeRequest{
		Token:              body.Token,
		NodeType:           body.NodeType,
		WireguardPublicKey: &body.WireguardPublicKey,
	}
	if body.NodeID != "" {
		req.NodeId = &body.NodeID
	}
	if body.Hostname != "" {
		req.Hostname = body.Hostname
	}
	if body.ExternalIP != "" {
		req.ExternalIp = &body.ExternalIP
	}
	if body.InternalIP != "" {
		req.InternalIp = &body.InternalIP
	}
	if body.TargetClusterID != "" {
		req.TargetClusterId = &body.TargetClusterID
	}
	if body.WireguardIP != "" {
		req.WireguardIp = &body.WireguardIP
	}
	if body.WireguardPort > 0 {
		req.WireguardPort = &body.WireguardPort
	}

	resp, err := h.qm.BootstrapInfrastructureNode(c.Request.Context(), req)
	if err != nil {
		// Log the full gRPC error server-side; return a controlled public
		// message keyed to the gRPC status code. Raw server errors never
		// leak to the bootstrap caller.
		h.logger.WithError(err).Warn("BootstrapInfrastructureNode proxy failed")
		httpStatus, publicMsg := mapGRPCErrorToHTTP(err)
		c.JSON(httpStatus, gin.H{"error": publicMsg})
		return
	}

	out := infraBootstrapResponse{
		NodeID:                resp.GetNodeId(),
		ClusterID:             resp.GetClusterId(),
		WireguardIP:           resp.GetWireguardIp(),
		WireguardPort:         resp.GetWireguardPort(),
		MeshCIDR:              resp.GetMeshCidr(),
		QuartermasterGRPCAddr: resp.GetQuartermasterGrpcAddr(),
		SeedServiceEndpoints:  map[string][]string{},
	}
	if resp.TenantId != nil && *resp.TenantId != "" {
		t := *resp.TenantId
		out.TenantID = &t
	}
	for _, p := range resp.GetSeedPeers() {
		out.SeedPeers = append(out.SeedPeers, infraBootstrapPeer{
			NodeName:   p.GetNodeName(),
			PublicKey:  p.GetPublicKey(),
			Endpoint:   p.GetEndpoint(),
			AllowedIPs: p.GetAllowedIps(),
			KeepAlive:  p.GetKeepAlive(),
		})
	}
	for svcType, endpoints := range resp.GetSeedServiceEndpoints() {
		out.SeedServiceEndpoints[svcType] = append([]string(nil), endpoints.GetIps()...)
	}
	if ca := resp.GetCaBundle(); len(ca) > 0 {
		out.CABundle = string(ca)
	}

	c.JSON(http.StatusOK, out)
}

// mapGRPCErrorToHTTP derives an HTTP status and a short public message from
// a gRPC error, using status.Code(err) so the mapping is based on the actual
// code rather than substring-matching the message. The returned message is
// safe to expose to public bootstrap callers; the caller should log the
// underlying error separately.
func mapGRPCErrorToHTTP(err error) (int, string) {
	st, _ := status.FromError(err)
	switch st.Code() {
	case codes.InvalidArgument:
		return http.StatusBadRequest, st.Message()
	case codes.Unauthenticated:
		return http.StatusUnauthorized, "bootstrap token rejected"
	case codes.PermissionDenied:
		return http.StatusForbidden, "bootstrap token rejected"
	case codes.NotFound:
		return http.StatusNotFound, st.Message()
	case codes.FailedPrecondition:
		return http.StatusPreconditionFailed, st.Message()
	case codes.ResourceExhausted:
		return http.StatusServiceUnavailable, "mesh address space exhausted; contact the operator"
	case codes.Unavailable:
		return http.StatusServiceUnavailable, "control plane unavailable, retry later"
	case codes.DeadlineExceeded:
		return http.StatusGatewayTimeout, "control plane timed out, retry later"
	case codes.AlreadyExists:
		return http.StatusConflict, st.Message()
	case codes.OK:
		// Shouldn't happen — if err is non-nil OK falls through. Return 500.
		return http.StatusInternalServerError, "internal server error"
	default:
		return http.StatusInternalServerError, "internal server error"
	}
}
