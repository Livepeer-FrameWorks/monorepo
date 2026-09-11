package triggers

import (
	"context"
	"errors"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"frameworks/api_balancing/internal/control"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/proto"
)

// SourceConnection contains authenticated emitting-node identity and observed
// remote connection data. RemoteAddress is not an authenticated pulling node.
type SourceConnection struct {
	SourceNodeID, SourceClusterID          string
	RuntimeName, RemoteAddress, RequestURL string
}

// SourceConnectionAdmission performs current authorization reads, not mutations
// or reservations: transport retries invoke it again for the same connection.
type SourceConnectionAdmission func(context.Context, SourceConnection) (time.Time, error)

// SetSourceConnectionAdmission installs the source gate before trigger handling
// starts. An absent gate always denies source connections routed through it.
func (p *Processor) SetSourceConnectionAdmission(admission SourceConnectionAdmission) {
	p.sourceConnectionAdmission = admission
}

func (p *Processor) handleConnPlay(trigger *ipcpb.MistTrigger) (string, bool, error) {
	connection := trigger.GetConnectionPlay()
	if trigger.GetTriggerType() != "CONN_PLAY" || !trigger.GetBlocking() || connection == nil ||
		trigger.GetNodeId() == "" || trigger.GetClusterId() == "" || connection.GetStreamName() == "" || proto.Size(connection) > 16<<10 {
		return "", true, errors.New("source connection identity is invalid")
	}
	if !strings.EqualFold(connection.GetConnector(), "DTSC") {
		return "true", false, nil
	}
	if p.sourceConnectionAdmission == nil {
		return "", true, errors.New("source connection admission is unavailable")
	}
	address, err := netip.ParseAddr(connection.GetHost())
	if err != nil || address.Zone() != "" || address.IsUnspecified() || address.IsMulticast() {
		return "", true, errors.New("source connection address is invalid")
	}
	u, err := url.Parse(connection.GetRequestUrl())
	if err != nil || (u.Scheme != "dtsc" && u.Scheme != "dtscs") || u.Host == "" ||
		u.Fragment != "" || u.User != nil || strings.TrimPrefix(u.Path, "/") != connection.GetStreamName() {
		return "", true, errors.New("source connection URL is invalid")
	}
	ctx := control.MediaRequestContext(context.Background(), "mist_conn_play")
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	expires, err := p.sourceConnectionAdmission(ctx, SourceConnection{
		SourceNodeID: trigger.GetNodeId(), SourceClusterID: trigger.GetClusterId(),
		RuntimeName: connection.GetStreamName(), RemoteAddress: address.Unmap().String(), RequestURL: connection.GetRequestUrl(),
	})
	if err != nil {
		return "", true, errors.New("source connection admission rejected")
	}
	if ctx.Err() != nil || !time.Now().Before(expires) {
		return "", true, errors.New("source connection admission expired")
	}
	return "true", false, nil
}
