package control

import (
	"context"
	"crypto/hmac"
	"strings"
	"time"
)

// acceptedPullAdmissionWindow bounds how long an accepted outbound pull admits
// the peer's DTSC connection at the origin after its last renewal. Source
// resolution renews acceptance before handing an older attempt to Mist;
// the window tolerates a replica that connects late but not an abandoned pull.
const acceptedPullAdmissionWindow = 5 * time.Minute

// AcceptedOutboundPull returns the pull this origin accepted for a peer
// destination when a DTSC connection now arrives at sourceNodeID for the
// stream. The connection is the prepared source path, not a viewer: it must
// present the credential for its exact destination and attempt, match the current
// active publisher generation, and be renewed within the admission window.
func (r *StreamRegistry) AcceptedOutboundPull(ctx context.Context, internalName, sourceNodeID, credential string, now time.Time) (OutboundPull, bool) {
	internalName = sourceInternalKey(internalName)
	sourceNodeID = strings.TrimSpace(sourceNodeID)
	if r == nil || internalName == "" || sourceNodeID == "" || credential == "" || len(credential) > 512 || now.IsZero() {
		return OutboundPull{}, false
	}
	entry, found, err := r.currentSourceEntry(ctx, internalName)
	if err != nil || !found {
		return OutboundPull{}, false
	}
	loc, ok := entry.LocalLocation(r.clusterID)
	if !ok || !loc.SourceActive || loc.OwnerNodeID != sourceNodeID || loc.SourceGeneration == "" {
		return OutboundPull{}, false
	}
	for _, pull := range loc.OutboundPullers {
		if !strings.HasPrefix(credential, sourcePullCredentialPrefix+pull.AttemptID+".") {
			continue
		}
		if pull.SourceNodeID != sourceNodeID || pull.SourceGeneration != loc.SourceGeneration || pull.SourceRevision != loc.SourceRevision {
			continue
		}
		// Only the abandonment bound is enforced, never "this renewal is newer
		// than my clock reading". The caller reads its clock before this entry
		// is fetched, so a renewal that lands during the fetch is stamped after
		// that reading; refusing it would deny the connection presenting the
		// current credential at exactly the moment its pull was renewed. A
		// forward-stamped renewal is not a bypass either: only this origin
		// writes the record, and the attempt credential, owner node, generation
		// and revision are all still matched below.
		if pull.UpdatedAt.IsZero() || !now.Before(pull.UpdatedAt.Add(acceptedPullAdmissionWindow)) {
			continue
		}
		want := sourcePullCredential(internalName, pull)
		if want == "" || !hmac.Equal([]byte(want), []byte(credential)) {
			continue
		}
		return pull, true
	}
	return OutboundPull{}, false
}
