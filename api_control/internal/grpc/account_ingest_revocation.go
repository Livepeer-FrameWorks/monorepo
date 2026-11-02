package grpc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
// pending; repeated stop commands are safe. A cell an operator retired settles
// only when it cannot be reached: a retired cell that answers with an error
// still keeps the stop pending.
func (s *CommodoreServer) stopAccountIngest(ctx context.Context, tenantID, streamID string, names []string) error {
	cells, err := commodoredb.New(s.db).ListAccountIngestCells(ctx, commodoredb.ListAccountIngestCellsParams{TenantID: tenantID, StreamID: streamID})
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	failures := make([]error, len(cells))
	unreachable := make([]bool, len(cells))
	for i, cell := range cells {
		wg.Add(1)
		go func() {
			defer wg.Done()
			callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			client, resolveErr := s.resolveFoghornForClusterDirect(callCtx, cell)
			if resolveErr != nil {
				failures[i] = fmt.Errorf("resolve historical ingest cell %s: %w", cell, resolveErr)
				unreachable[i] = true
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
				unreachable[i] = ingestCellUnreachable(stopErr)
			}
		}()
	}
	wg.Wait()
	return s.settleRetiredIngestCells(ctx, tenantID, cells, failures, unreachable)
}

// settleRetiredIngestCells drops the failures of unreachable retired cells. A
// failed retirement read keeps every failure, so the stop stays pending.
func (s *CommodoreServer) settleRetiredIngestCells(ctx context.Context, tenantID string, cells []string, failures []error, unreachable []bool) error {
	var candidates []string
	for i, failure := range failures {
		if failure != nil && unreachable[i] {
			candidates = append(candidates, cells[i])
		}
	}
	if len(candidates) == 0 {
		return errors.Join(failures...)
	}
	retiredCells, err := commodoredb.New(s.db).ListRetiredIngestCells(ctx, candidates)
	if err != nil {
		return errors.Join(append(failures, fmt.Errorf("read retired ingest cells: %w", err))...)
	}
	retired := make(map[string]bool, len(retiredCells))
	for _, cell := range retiredCells {
		retired[cell] = true
	}
	for i, failure := range failures {
		if failure == nil || !unreachable[i] || !retired[cells[i]] {
			continue
		}
		failures[i] = nil
		s.logger.WithFields(logging.Fields{"tenant_id": tenantID, "cell": cells[i]}).WithError(failure).Info("Session stop settled for an unreachable retired cell")
	}
	return errors.Join(failures...)
}

// ingestCellUnreachable reports whether a call failed without the cell
// answering. Any answer, including Unimplemented, means the cell is running.
func ingestCellUnreachable(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded:
		return true
	default:
		return false
	}
}
