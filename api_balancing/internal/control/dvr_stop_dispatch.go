package control

// DVRStopDispatchHint wakes the existing recovery worker after a committed stop.
// The database remains authoritative when hints are dropped or the process exits.
type DVRStopDispatchHint struct {
	Claims     []DVRStopClaim
	TenantID   string
	Generation string
}

var dvrStopDispatchHints = make(chan DVRStopDispatchHint, 64)

func DVRStopDispatchHints() <-chan DVRStopDispatchHint { return dvrStopDispatchHints }

func QueueDVRStopClaims(claims []DVRStopClaim) {
	if len(claims) == 0 {
		return
	}
	queueDVRStopDispatch(DVRStopDispatchHint{Claims: append([]DVRStopClaim(nil), claims...)})
}

func QueueDVRStopsForGeneration(tenantID, generation string) {
	if tenantID == "" || generation == "" {
		return
	}
	queueDVRStopDispatch(DVRStopDispatchHint{TenantID: tenantID, Generation: generation})
}

func queueDVRStopDispatch(hint DVRStopDispatchHint) {
	select {
	case dvrStopDispatchHints <- hint:
	default:
	}
}
