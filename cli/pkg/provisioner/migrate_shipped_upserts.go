package provisioner

import (
	"crypto/sha256"
	"fmt"
)

// shippedExpandUpserts pins SQL that shipped before expand upserts were checked.
// These files cannot be edited. The exception applies only to their exact bytes
// at their original paths, never to modified files or copies in another release.
var shippedExpandUpserts = map[string]string{
	"migrations/foghorn/v0.3.0/expand/014_key_scoped_ordering_counters.sql":    "432eb69637ed8ccb8a5d134b084567bf80e78b0bdf65f5700ea4c7aa84481e37",
	"migrations/purser/v0.3.0/expand/001_dimensioned_usage_reports.sql":        "2a1e5972c3d47d78e98d4e31845cfca67e13eeeb58f5bb8122f8e95325d484ac",
	"migrations/purser/v0.3.0/expand/027_reseed_delivery_meter_dimensions.sql": "94969b2d31f9f5a226b4d2c18deb52fb6e1ad509eee93534e456b827e3f720b0",
}

func immutableShippedUpsert(migration Migration) bool {
	checksum, ok := shippedExpandUpserts[migration.Path]
	return ok && fmt.Sprintf("%x", sha256.Sum256([]byte(migration.content))) == checksum
}
