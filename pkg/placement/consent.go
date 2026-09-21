package placement

import placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"

// ConsentVerbs returns the placement verbs a capacity owner's consent permits,
// in Ingest, Serve order, or nil when it permits neither. Foghorn placement authority, Commodore previews, and
// Quartermaster capability reports all derive verbs through this function, so
// a cluster is described as accepting exactly what placement will admit.
func ConsentVerbs(consent *placementpb.CapacityConsent) []Verb {
	var verbs []Verb
	if consent.GetAllowIngest() {
		verbs = append(verbs, Ingest)
	}
	if consent.GetAllowServe() {
		verbs = append(verbs, Serve)
	}
	return verbs
}
