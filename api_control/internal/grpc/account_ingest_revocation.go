package grpc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
)

// stopRevokedStreamIngest runs before authority compilation, so an unavailable
// billing source cannot prevent delivery of an account's ingest revocation.
func (s *CommodoreServer) stopRevokedStreamIngest(ctx context.Context, tenantID, streamID string) error {
	name, err := commodoredb.New(s.db).GetRevokedStreamIngest(ctx, commodoredb.GetRevokedStreamIngestParams{TenantID: tenantID, StreamID: streamID})
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.stopAccountIngest(ctx, tenantID, streamID, []string{name})
}

// stopAccountIngest contacts every recorded owning cell independently of the
// tenant's current entitlements. Any failed delivery leaves the durable work
// pending; repeated stop commands are safe.
func (s *CommodoreServer) stopAccountIngest(ctx context.Context, tenantID, streamID string, names []string) error {
	cells, err := commodoredb.New(s.db).ListAccountIngestCells(ctx, commodoredb.ListAccountIngestCellsParams{TenantID: tenantID, StreamID: streamID})
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	failures := make([]error, len(cells))
	for i, cell := range cells {
		wg.Add(1)
		go func() {
			defer wg.Done()
			callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			client, resolveErr := s.resolveFoghornForClusterDirect(callCtx, cell)
			if resolveErr != nil {
				failures[i] = fmt.Errorf("resolve historical ingest cell %s: %w", cell, resolveErr)
				return
			}

			if streamID != "" {
				_, readErr := commodoredb.New(s.db).GetRevokedStreamIngest(callCtx, commodoredb.GetRevokedStreamIngestParams{TenantID: tenantID, StreamID: streamID})
				if errors.Is(readErr, sql.ErrNoRows) {
					return
				}
				if readErr != nil {
					failures[i] = readErr
					return
				}
			}
			var stopErr error
			if len(names) == 0 {
				_, _, stopErr = client.TerminateTenantStreams(callCtx, tenantID, sharedauthority.ReasonAccountInactive)
			} else {
				_, _, stopErr = client.TerminateNamedTenantStreams(callCtx, tenantID, sharedauthority.ReasonAccountInactive, names)
			}
			if stopErr != nil {
				failures[i] = fmt.Errorf("stop ingest on cell %s: %w", cell, stopErr)
			}
		}()
	}
	wg.Wait()
	return errors.Join(failures...)
}
