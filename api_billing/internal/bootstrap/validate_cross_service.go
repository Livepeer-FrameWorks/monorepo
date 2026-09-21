package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"frameworks/api_billing/internal/database/purserdb"
	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
)

// ValidatePlatformOfficialPricingCoverage is the cross-service invariant
// `purser bootstrap validate` enforces: every cluster Quartermaster reports as
// `is_platform_official: true` must have a matching `purser.cluster_pricing`
// row. Without that row, ensureTierClusterAccess silently hands out empty
// tenant_cluster_access and the deposit monitor goes blind.
//
// qm carries the same address, service token, and TLS posture
// (GRPC_ALLOW_INSECURE, GRPC_TLS_CA_PATH, QUARTERMASTER_GRPC_TLS_SERVER_NAME)
// as the Purser server's Quartermaster client, so the validator never
// downgrades the in-cluster TLS posture.
//
// Returns the cluster IDs that are missing pricing. An empty slice = clean.
func ValidatePlatformOfficialPricingCoverage(
	ctx context.Context,
	db *sql.DB,
	qm qmclient.GRPCConfig,
) ([]string, error) {
	if db == nil {
		return nil, errors.New("ValidatePlatformOfficialPricingCoverage: nil db")
	}

	client, err := qmclient.NewGRPCClient(qm)
	if err != nil {
		return nil, fmt.Errorf("connect Quartermaster at %s: %w", qm.GRPCAddr, err)
	}
	defer client.Close()

	resp, err := client.ListClusters(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("ListClusters: %w", err)
	}

	pricedIDs, err := loadPricedClusterIDs(ctx, db)
	if err != nil {
		return nil, err
	}

	var missing []string
	for _, c := range resp.GetClusters() {
		if !c.GetIsPlatformOfficial() {
			continue
		}
		id := c.GetClusterId()
		if !pricedIDs[id] {
			missing = append(missing, id)
		}
	}
	return missing, nil
}

func loadPricedClusterIDs(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	ids, err := purserdb.New(db).ListBootstrapPricedClusterIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("load cluster_pricing ids: %w", err)
	}
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}
