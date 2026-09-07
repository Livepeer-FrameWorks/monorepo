package datamigrations

import (
	"context"
	"fmt"
	"strings"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"
)

const NodeIdentityKeysID = "quartermaster_node_identity_keys_v0_3_0"

const activeKeylessNodeCountSQL = `
SELECT COUNT(*)
FROM quartermaster.node_fingerprints fingerprint
JOIN quartermaster.infrastructure_nodes node ON node.node_id = fingerprint.node_id
JOIN quartermaster.infrastructure_clusters cluster ON cluster.cluster_id = node.cluster_id
WHERE node.status = 'active'
  AND cluster.is_active = true
  AND (
      fingerprint.node_identity_public_key_ed25519 IS NULL
      OR octet_length(fingerprint.node_identity_public_key_ed25519) <> 32
  )`

const activeKeylessNodeIDsSQL = `
SELECT node.node_id
FROM quartermaster.node_fingerprints fingerprint
JOIN quartermaster.infrastructure_nodes node ON node.node_id = fingerprint.node_id
JOIN quartermaster.infrastructure_clusters cluster ON cluster.cluster_id = node.cluster_id
WHERE node.status = 'active'
  AND cluster.is_active = true
  AND (
      fingerprint.node_identity_public_key_ed25519 IS NULL
      OR octet_length(fingerprint.node_identity_public_key_ed25519) <> 32
  )
ORDER BY node.node_id
LIMIT 100`

func registerNodeIdentityKeysMigration() {
	datamigrate.Register(datamigrate.Migration{
		ID:                  NodeIdentityKeysID,
		Service:             "quartermaster",
		IntroducedIn:        "v0.3.0",
		RequiredBeforePhase: "postdeploy",
		Description:         "require active enrolled nodes to have token-authorized Ed25519 identity keys",
		Run:                 inspectNodeIdentityKeys,
		Verify:              verifyNodeIdentityKeys,
	})
}

// inspectNodeIdentityKeys never invents or binds identity keys. It reports the
// number of active rows that require operator-driven token re-enrollment; the
// verification gate blocks postdeploy until that count reaches zero.
func inspectNodeIdentityKeys(ctx context.Context, db datamigrate.DB, _ datamigrate.RunOptions) (datamigrate.Progress, error) {
	count, err := countActiveKeylessNodes(ctx, db)
	if err != nil {
		return datamigrate.Progress{}, err
	}
	return datamigrate.Progress{Scanned: count, Skipped: count, Done: true}, nil
}

func verifyNodeIdentityKeys(ctx context.Context, db datamigrate.DB) error {
	count, err := countActiveKeylessNodes(ctx, db)
	if err != nil {
		return err
	}
	if count != 0 {
		nodeIDs, listErr := listActiveKeylessNodeIDs(ctx, db)
		if listErr != nil {
			return fmt.Errorf("%d active enrolled nodes require token-authorized identity-key recovery (list node IDs: %w)", count, listErr)
		}
		suffix := ""
		if count > int64(len(nodeIDs)) {
			suffix = fmt.Sprintf(" (showing first %d)", len(nodeIDs))
		}
		return fmt.Errorf("%d active enrolled nodes require token-authorized identity-key recovery; node IDs%s: %s", count, suffix, strings.Join(nodeIDs, ", "))
	}
	return nil
}

func listActiveKeylessNodeIDs(ctx context.Context, db datamigrate.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, activeKeylessNodeIDsSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var nodeIDs []string
	for rows.Next() {
		var nodeID string
		if err := rows.Scan(&nodeID); err != nil {
			return nil, err
		}
		nodeIDs = append(nodeIDs, nodeID)
	}
	return nodeIDs, rows.Err()
}

func countActiveKeylessNodes(ctx context.Context, db datamigrate.DB) (int64, error) {
	var count int64
	if err := db.QueryRowContext(ctx, activeKeylessNodeCountSQL).Scan(&count); err != nil {
		return 0, fmt.Errorf("count active keyless node fingerprints: %w", err)
	}
	return count, nil
}
