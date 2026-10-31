package provisioner

import (
	"crypto/sha256"
	"fmt"
)

// Exact shipped files are immutable even when their inline constraints would
// now require staged validation. Copies and changed bytes receive no exception.
var shippedInlineConstraints = map[string]string{
	"migrations/commodore/v0.3.11/expand/018_restream_video_selection.sql": "a755fbea46316d662e4065934df952055f25ff23bec66b06e1765f959024a730",
	"migrations/purser/v0.3.0/expand/002_invoice_delivery_outboxes.sql":    "e4343699609c4ffc8f8032efcc4b3892c898f27bf246b30496cc709a5a817d14",
	"migrations/purser/v0.3.0/expand/006_x402_v2_quotes.sql":               "830b5f3a3479e6d6917cff39d74dcee79e2d6fbabf0babcc6d26dc55450a00d3",
}

func immutableShippedInlineConstraint(migration Migration) bool {
	checksum, ok := shippedInlineConstraints[migration.Path]
	return ok && fmt.Sprintf("%x", sha256.Sum256([]byte(migration.content))) == checksum
}
