package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	qmclient "frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/config"
	"frameworks/pkg/logging"
)

// ValidatePlatformOfficialPricingCoverage is the cross-service invariant
// `purser bootstrap validate` enforces: every cluster Quartermaster reports as
// `is_platform_official: true` must have a matching `purser.cluster_pricing`
// row. Without that row, ensureTierClusterAccess silently hands out empty
// tenant_cluster_access and the deposit monitor goes blind.
//
// Returns the cluster IDs that are missing pricing. An empty slice = clean.
func ValidatePlatformOfficialPricingCoverage(
	ctx context.Context,
	db *sql.DB,
	qmAddr, serviceToken string,
	logger logging.Logger,
) ([]string, error) {
	if db == nil {
		return nil, errors.New("ValidatePlatformOfficialPricingCoverage: nil db")
	}

	// TLS posture mirrors the runtime client config used by the Purser server
	// (see api_billing/cmd/purser/main.go's QM client setup): same
	// GRPC_ALLOW_INSECURE / GRPC_TLS_CA_PATH / GRPC_TLS_SERVER_NAME envs. Hard-
	// coding AllowInsecure here would have made `purser bootstrap validate`
	// silently downgrade the in-cluster TLS posture, defeating the certs the
	// runtime gRPC chain depends on.
	qm, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{
		GRPCAddr:      qmAddr,
		ServiceToken:  serviceToken,
		Logger:        logger,
		AllowInsecure: config.GetEnvBool("GRPC_ALLOW_INSECURE", false),
		CACertFile:    config.GetEnv("GRPC_TLS_CA_PATH", ""),
		ServerName:    config.GetEnv("GRPC_TLS_SERVER_NAME", ""),
	})
	if err != nil {
		return nil, fmt.Errorf("connect Quartermaster at %s: %w", qmAddr, err)
	}
	defer qm.Close()

	resp, err := qm.ListClusters(ctx, nil)
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
	rows, err := db.QueryContext(ctx, `SELECT cluster_id FROM purser.cluster_pricing`)
	if err != nil {
		return nil, fmt.Errorf("load cluster_pricing ids: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only

	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan cluster_pricing id: %w", err)
		}
		out[id] = true
	}
	return out, rows.Err()
}
