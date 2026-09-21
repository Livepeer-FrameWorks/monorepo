package qmbootstrap

import (
	"fmt"
	"strconv"

	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/version"
)

// ServiceRegistration describes the instance a service registers with
// Quartermaster at startup.
type ServiceRegistration struct {
	// ServiceType is the canonical pkg/servicedefs ID.
	ServiceType string
	// Protocol is the registered endpoint protocol. Empty means "http".
	Protocol string
	// Port is the advertised port for Protocol.
	Port          string
	AdvertiseHost string
	ClusterID     string
	NodeID        string
	// OmitHealthEndpoint registers the instance without a health endpoint,
	// for services whose registration never carried one.
	OmitHealthEndpoint bool
}

// NewServiceRequest builds the bootstrap request. Unless OmitHealthEndpoint is
// set, the advertised health endpoint is the service's readiness path from
// pkg/servicedefs, the same path the CLI rollout gate, doctor probe, and
// rendered bootstrap desired state use.
func NewServiceRequest(reg ServiceRegistration) (*quartermasterpb.BootstrapServiceRequest, error) {
	def, ok := servicedefs.Lookup(reg.ServiceType)
	if !ok {
		return nil, fmt.Errorf("quartermaster bootstrap: unknown service %q", reg.ServiceType)
	}
	port, err := strconv.Atoi(reg.Port)
	if err != nil || port <= 0 || port > 65535 {
		return nil, fmt.Errorf("quartermaster bootstrap: invalid port %q", reg.Port)
	}
	protocol := reg.Protocol
	if protocol == "" {
		protocol = "http"
	}
	advertiseHost := reg.AdvertiseHost
	req := &quartermasterpb.BootstrapServiceRequest{
		Type:          reg.ServiceType,
		Version:       version.Version,
		Protocol:      protocol,
		Port:          int32(port),
		AdvertiseHost: &advertiseHost,
	}
	if !reg.OmitHealthEndpoint {
		healthEndpoint := def.ReadinessPath()
		req.HealthEndpoint = &healthEndpoint
	}
	if reg.ClusterID != "" {
		clusterID := reg.ClusterID
		req.ClusterId = &clusterID
	}
	if reg.NodeID != "" {
		nodeID := reg.NodeID
		req.NodeId = &nodeID
	}
	return req, nil
}
