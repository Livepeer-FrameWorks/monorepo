package coordinator

import (
	"strings"

	"frameworks/api_network_observer/internal/store"
	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
)

// Gateway observer location sources. A node position is Quartermaster's
// GeoIP lookup of the node's external IP; a cluster position is the mean of
// the located nodes in the gateway node's cluster, the same centroid the
// network map draws for that cluster.
const (
	GatewayLocationNode    = "node"
	GatewayLocationCluster = "cluster"
)

// NodeLocation is one Quartermaster node and its stored position, if any.
type NodeLocation struct {
	NodeID    string
	ClusterID string
	Latitude  float64
	Longitude float64
	Known     bool
}

// LocateGateways positions each gateway observer at the Quartermaster node
// its gateway ID names, or at that node's cluster centroid when the node has
// no position. A gateway whose node and cluster have no position is unknown;
// no coordinates are invented from region labels. A location from the
// configured observer definitions is kept, and a gateway whose node is absent
// from nodes keeps its previous location.
func LocateGateways(s *store.State, nodes []NodeLocation) {
	byNode := make(map[string]NodeLocation, len(nodes))
	type centroid struct {
		lat, lon float64
		count    int
	}
	clusters := map[string]*centroid{}
	for _, n := range nodes {
		byNode[n.NodeID] = n
		if !n.Known || !(obs.Location{Known: true, Latitude: n.Latitude, Longitude: n.Longitude}).Valid() {
			continue
		}
		c := clusters[n.ClusterID]
		if c == nil {
			c = &centroid{}
			clusters[n.ClusterID] = c
		}
		c.lat += n.Latitude
		c.lon += n.Longitude
		c.count++
	}
	for id, o := range s.Observers {
		if o.Kind != "gateway" {
			continue
		}
		if o.Location.Valid() && o.Location.Source != GatewayLocationNode && o.Location.Source != GatewayLocationCluster {
			continue
		}
		n, ok := byNode[strings.TrimPrefix(id, "gateway:")]
		if !ok {
			continue
		}
		location := obs.Location{Source: "unknown"}
		if candidate := (obs.Location{Known: n.Known, Latitude: n.Latitude, Longitude: n.Longitude, Source: GatewayLocationNode}); candidate.Valid() {
			location = candidate
		} else if c := clusters[n.ClusterID]; c != nil {
			location = obs.Location{Known: true, Latitude: c.lat / float64(c.count), Longitude: c.lon / float64(c.count), Source: GatewayLocationCluster}
		}
		o.Location = location
		s.Observers[id] = o
	}
}
