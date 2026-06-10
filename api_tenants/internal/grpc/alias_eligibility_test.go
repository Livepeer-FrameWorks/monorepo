package grpc

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/models"
)

// The SQL predicate and the Go allowlist gate the same decision from two
// sides (backstop SQL vs enqueue-time Go); this pins them together so a tier
// added to one cannot silently drift from the other.
func TestSQLAliasTierEligibleMatchesModelAllowlist(t *testing.T) {
	quoted := make([]string, len(models.AliasEligibleDeploymentTiers))
	for i, tier := range models.AliasEligibleDeploymentTiers {
		quoted[i] = fmt.Sprintf("'%s'", tier)
	}
	want := fmt.Sprintf("t.deployment_tier IN (%s)", strings.Join(quoted, ", "))
	if sqlAliasTierEligible != want {
		t.Errorf("sqlAliasTierEligible = %q, want %q (must enumerate exactly models.AliasEligibleDeploymentTiers)", sqlAliasTierEligible, want)
	}
}
