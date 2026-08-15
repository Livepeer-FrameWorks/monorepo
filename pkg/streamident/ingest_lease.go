package streamident

import "time"

// ActiveIngestLease is how long a cluster's claim on a stream's ingest holds
// without being re-asserted.
//
// It is a contract between two services and lives here so neither can drift
// from the other: Commodore enforces it (the contended-update guard on
// commodore.streams.active_ingest_cluster_id, and the freshness test that makes
// a claim authoritative for routing), and Foghorn's placement renewal must
// re-assert every live publisher's claim strictly inside it. Changing the value
// changes both sides at once, which is the point — an independently hardcoded
// copy on either side is how a renewal cadence silently stops defending the
// lease it was sized against.
const ActiveIngestLease = 30 * time.Second

// MaxPublishersPerFoghorn is the shared hard admission ceiling defended by one replica's renewal
// budget and by Helmsman's durable generation-fence store. The placement job can renew this many
// claims inside ActiveIngestLease at its bounded worst case.
const MaxPublishersPerFoghorn = 32000
