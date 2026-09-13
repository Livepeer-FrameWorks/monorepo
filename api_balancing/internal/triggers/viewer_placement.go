package triggers

import (
	"context"
	"encoding/hex"
	"errors"
	"math"
	"strings"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/federation"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// acceptedSourcePull reports whether a connection at nodeID is the DTSC pull this
// origin accepted for a peer destination (NotifyOriginPull) against the node's
// current publisher generation. Such a connection is the prepared source path
// placement arranged, not a viewer: final viewer admission would otherwise
// evaluate the origin node against the viewer policy and refuse the replica.
// Only the DTSC connector qualifies, and only while the accepted pull is current.
func (p *Processor) acceptedSourcePull(ctx context.Context, internalName, nodeID, connector, requestURL string) bool {
	if !strings.EqualFold(strings.TrimSpace(connector), "DTSC") {
		return false
	}
	registry := p.preparedSourceRegistry
	if registry == nil {
		registry = control.StreamRegistryInstance
	}
	if registry == nil {
		return false
	}
	pull, ok := registry.AcceptedOutboundPull(ctx, internalName, nodeID, control.SourcePullCredential(requestURL), time.Now())
	if ok && p.logger != nil {
		p.logger.WithFields(map[string]any{"internal_name": internalName, "node_id": nodeID, "dest_cluster": pull.DestClusterID, "dest_node": pull.DestNodeID}).Info("Admitting DTSC connection as accepted origin pull")
	}
	return ok
}

// acceptedProcessingSourceRead reports whether a Mist HTTP output at nodeID is
// Helmsman staging a processing job's node-local source (a /view cut of the
// live buffer, rolling DVR or chapter) with the credential Foghorn minted for
// that job (ProcessingSourceCredential). Such a read is the platform producing
// an artifact on the node that holds the source bytes, not a viewer: without
// this, a serve policy that does not prefer the ingest cluster refuses the
// ingest node its own buffer and every clip from live fails to stage. The DTSC
// connector is the origin-pull path and never qualifies here.
func (p *Processor) acceptedProcessingSourceRead(tenantID, internalName, nodeID, connector, requestURL string) (string, bool) {
	if strings.EqualFold(strings.TrimSpace(connector), "DTSC") || strings.TrimSpace(connector) == "" {
		return "", false
	}
	artifactHash, ok := control.AcceptedProcessingSourceRead(requestURL, tenantID, internalName, nodeID, time.Now())
	if ok && p.logger != nil {
		p.logger.WithFields(map[string]any{"internal_name": internalName, "node_id": nodeID, "connector": connector, "artifact_hash": artifactHash}).Info("Admitting Mist output as processing source read")
	}
	return artifactHash, ok
}

// ViewerPlacementConnection contains server-resolved identity and Mist connection
// evidence. A resolver must derive canonical protocol, signed object identity and
// owner generation itself; URL query parameters and prefilled geo are not inputs.
type ViewerPlacementConnection struct {
	TenantID, InternalName, ClusterID, NodeID string
	Connector, ClientAddress                  string
	PreSource                                 bool
}

func (connection ViewerPlacementConnection) protocol() (string, error) {
	if connection.PreSource {
		return mist.PlayRewriteProtocol(connection.Connector)
	}
	return mist.ViewerProtocol(connection.Connector)
}

type ViewerPlacementAdmission func(context.Context, ViewerPlacementConnection) (federation.PlacementAdmissionDecision, error)

// SetViewerPlacementAdmission configures pre-source and final viewer admission before processing
// starts. Calling it requires placement even when the adapter is unavailable;
// clearing an installed adapter cannot restore the legacy public-marker bypass.
func (p *Processor) SetViewerPlacementAdmission(admission ViewerPlacementAdmission) {
	p.viewerPlacementRequired = true
	p.viewerPlacementAdmission = admission
}

func (p *Processor) checkViewerPlacement(ctx context.Context, tenantID, internalName, clusterID, nodeID string, viewer *ipcpb.ViewerConnectTrigger) (*federation.PlacementAdmissionDecision, error) {
	return p.checkPlacementConnection(ctx, ViewerPlacementConnection{TenantID: tenantID, InternalName: internalName, ClusterID: clusterID, NodeID: nodeID,
		Connector: viewer.GetConnector(), ClientAddress: viewer.GetHost()})
}

func (p *Processor) checkPlacementConnection(ctx context.Context, connection ViewerPlacementConnection) (*federation.PlacementAdmissionDecision, error) {
	tenantID, internalName, clusterID, nodeID := connection.TenantID, connection.InternalName, connection.ClusterID, connection.NodeID
	if p.viewerPlacementAdmission == nil {
		return nil, errors.New("viewer placement admission is unavailable")
	}
	for _, identity := range []string{tenantID, internalName, clusterID, nodeID} {
		if identity == "" || identity != strings.TrimSpace(identity) {
			return nil, errors.New("viewer placement identity is unavailable")
		}
	}
	protocol, protocolErr := connection.protocol()
	if protocolErr != nil {
		return nil, protocolErr
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	started := time.Now()
	decision, err := p.viewerPlacementAdmission(ctx, connection)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	digest, digestErr := hex.DecodeString(decision.PolicyDigest)
	if decision.TenantID != tenantID || decision.InternalName != internalName || decision.ClusterID != clusterID || decision.NodeID != nodeID || decision.Verb != placement.Serve ||
		decision.ObjectID == "" || decision.SourceGeneration == "" || decision.Protocol != protocol ||
		decision.PolicyRevision > math.MaxInt64 || decision.ParentRevision > math.MaxInt64 || digestErr != nil || len(digest) != 32 || hex.EncodeToString(digest) != decision.PolicyDigest ||
		decision.TenantAuthorityVersion <= 0 || decision.ObjectAuthorityVersion <= 0 ||
		!time.Now().Before(decision.ExpiresAt) || decision.ExpiresAt.After(started.Add(placement.PreparationLifetime)) {
		return nil, errors.New("viewer placement decision is expired or not bound to this connection")
	}
	return &decision, nil
}
