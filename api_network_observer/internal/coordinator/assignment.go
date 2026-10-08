package coordinator

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"frameworks/api_network_observer/internal/store"
	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
	"time"
)

func assignmentSignature(secret string, a obs.Assignment) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%s|%s|%s|%d|%d", a.ID, a.Endpoint.ID, a.ObserverID, a.DueAt.UnixNano(), a.Deadline.UnixNano())
	return hex.EncodeToString(mac.Sum(nil))
}

// Signed assignment snapshots permit durable late delivery after scheduler rows expire.
// The shared ingest credential remains the only prober enrollment credential.
func (a *API) resolveAssignments(s *store.State, b obs.Batch, now time.Time) error {
	for _, v := range b.Results {
		if _, ok := s.Assignments[v.AssignmentID]; ok {
			continue
		}
		if v.Assignment == nil {
			return fmt.Errorf("assignment snapshot required for delayed result")
		}
		snapshot := *v.Assignment
		expected := assignmentSignature(a.Secret, snapshot)
		if !hmac.Equal([]byte(snapshot.Signature), []byte(expected)) || snapshot.ID != v.AssignmentID || snapshot.ObserverID != b.ObserverID || snapshot.Endpoint.ID != v.EndpointID {
			return fmt.Errorf("invalid signed assignment")
		}
		if snapshot.Deadline.Sub(snapshot.DueAt) > time.Minute || now.Sub(snapshot.DueAt) > 30*24*time.Hour || snapshot.DueAt.After(now) {
			return fmt.Errorf("assignment outside retention")
		}
		if _, ok := s.Endpoints[v.EndpointID]; !ok {
			return fmt.Errorf("endpoint outside retention")
		}
		snapshot.Settled = true
		s.Assignments[snapshot.ID] = snapshot
	}
	return nil
}
