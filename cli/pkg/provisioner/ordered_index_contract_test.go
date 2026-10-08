package provisioner

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Yugabyte hashes the first index key when its ordering is omitted. A leading
// timestamp must preserve range scans for queue claims and retention sweeps.
func TestLeadingTimestampIndexesDeclareRangeOrdering(t *testing.T) {
	index := regexp.MustCompile(`(?is)CREATE (?:UNIQUE )?INDEX IF NOT EXISTS (\w+)\s+ON\s+[\w.]+\s*\(\s*(\w+)([^;]*);`)
	ordered := regexp.MustCompile(`(?i)^\s+(ASC|DESC)\b`)
	for _, database := range []string{"bosun", "commodore", "foghorn", "lookout", "navigator", "periscope", "purser", "quartermaster", "skipper"} {
		schema := readRepoFile(t, filepath.Join("pkg/database/sql/schema", database+".sql"))
		for _, match := range index.FindAllStringSubmatch(schema, -1) {
			isTime := strings.HasSuffix(match[2], "_at") || match[2] == "next_billing_date" || match[2] == "federated_purge_lease_until" || match[2] == "retention_until" || match[2] == "last_updated" || match[2] == "valid_until"
			if isTime && !ordered.MatchString(match[3]) {
				t.Errorf("%s.%s: leading timestamp %s would default to HASH on Yugabyte", database, match[1], match[2])
			}
		}
	}
}
