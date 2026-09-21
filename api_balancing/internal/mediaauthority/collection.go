package mediaauthority

import (
	"context"
	"fmt"
	"time"

	"frameworks/api_balancing/internal/artifacts"
	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
)

const (
	collectionInterval = time.Hour
	collectionBatch    = 500
	collectionPasses   = 40
	// Issue times are stamped by the control plane and compared here; the margin
	// covers the two clocks disagreeing.
	collectionClockMargin = time.Hour
)

// HeldSummary summarizes the authorities this cell holds as valid at asOf, in
// the form the control plane compares with what it has on record as delivered
// here.
func (s *Store) HeldSummary(ctx context.Context, asOf time.Time) (sharedauthority.HeldSummary, error) {
	var summary sharedauthority.HeldSummary
	if s == nil || s.db == nil {
		return summary, fmt.Errorf("media authority store is unavailable")
	}
	queries := foghorndb.New(s.db)
	page := foghorndb.ListHeldMediaAuthorityPageParams{AsOf: asOf.UTC(), PageSize: sharedauthority.RecoveryPageSize}
	for {
		rows, err := queries.ListHeldMediaAuthorityPage(ctx, page)
		if err != nil {
			return summary, fmt.Errorf("list held media authorities: %w", err)
		}
		for _, row := range rows {
			summary.Add(row.AuthorityKind, row.AuthorityID, row.AuthorityVersion)
			page.AfterKind, page.AfterID = row.AuthorityKind, row.AuthorityID
		}
		if len(rows) < int(page.PageSize) {
			break
		}
	}
	return summary, nil
}

// RunCollection forgets authorities nobody uses any more. The control plane
// stops renewing an object that has not been decided on for a while, and the
// copy here then runs out; without this the cell would keep a row for every
// object it ever held. A forgotten object is asked for again the next time a
// decision needs it, exactly like one this cell never held.
//
// Forgetting an authority also forgets the version this cell had reached, which
// is what refuses an older signed version. That is safe only once no older
// version can still verify: validity is bounded by MaxMediaObjectValidity, so
// an authority issued longer ago than that has no valid predecessor left. The
// bound also covers tenants, whose validity is shorter. A tombstone is never
// forgotten: it is what refuses the object's return.
func (s *Store) RunCollection(ctx context.Context, logger logging.Logger) {
	if s == nil || s.db == nil {
		return
	}
	collect := func() {
		collected, err := s.collectExpired(ctx)
		if err != nil && ctx.Err() == nil {
			logger.WithError(err).Warn("Failed to forget expired media authorities")
		}
		if collected > 0 {
			logger.WithField("authorities", collected).Info("Forgot media authorities that ran out unused")
		}
	}
	collect()
	ticker := time.NewTicker(collectionInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			collect()
		}
	}
}

func (s *Store) collectExpired(ctx context.Context) (int, error) {
	issuedBefore := s.now().UTC().Add(-sharedauthority.MaxMediaObjectValidity - collectionClockMargin)
	collected := 0
	for pass := 0; pass < collectionPasses && ctx.Err() == nil; pass++ {
		rows, err := foghorndb.New(s.db).ListCollectableMediaAuthorities(ctx, foghorndb.ListCollectableMediaAuthoritiesParams{
			IssuedBefore: issuedBefore, BatchSize: collectionBatch,
		})
		if err != nil {
			return collected, fmt.Errorf("list collectable media authorities: %w", err)
		}
		forgotten := 0
		for _, row := range rows {
			ok, collectErr := s.collectOne(ctx, row)
			if collectErr != nil {
				return collected, collectErr
			}
			if ok {
				forgotten++
			}
		}
		collected += forgotten
		// A short batch is the end. A full batch that forgot nothing is rows an
		// apply is holding; the next run takes them.
		if len(rows) < collectionBatch || forgotten == 0 {
			break
		}
	}
	return collected, nil
}

// collectOne forgets one authority under the locks Apply takes, in the order
// Apply takes them, fenced on the version that was found collectable. An apply
// that advanced the authority in between wins, and the row stays.
func (s *Store) collectOne(ctx context.Context, row foghorndb.ListCollectableMediaAuthoritiesRow) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin media authority collection: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // best effort after commit/error
	queries := foghorndb.New(tx)
	if err = queries.SetLocalMediaAuthorityLockTimeout(ctx, mediaAuthorityLockTimeout.String()); err != nil {
		return false, fmt.Errorf("bound media authority lock wait: %w", err)
	}
	if row.ArtifactHash != "" {
		if err = queries.LockThumbnailAsset(ctx, foghorndb.LockThumbnailAssetParams{
			LockNamespace: artifacts.ThumbnailAssetLockNamespace, AssetKey: row.ArtifactHash,
		}); err != nil {
			// Held by an apply or a purge; this authority is taken on a later run.
			return false, nil //nolint:nilerr // contention is not a failure of the collection
		}
	}
	if err = queries.LockMediaAuthority(ctx, foghorndb.LockMediaAuthorityParams{
		LockNamespace: mediaAuthorityLockNamespace, AuthorityKind: row.AuthorityKind, AuthorityID: row.AuthorityID,
	}); err != nil {
		return false, nil //nolint:nilerr // contention is not a failure of the collection
	}
	deleted, err := queries.DeleteCollectedMediaAuthority(ctx, foghorndb.DeleteCollectedMediaAuthorityParams{
		AuthorityKind: row.AuthorityKind, AuthorityID: row.AuthorityID, AuthorityVersion: row.AuthorityVersion,
	})
	if err != nil {
		return false, fmt.Errorf("forget media authority %s: %w", row.AuthorityID, err)
	}
	if deleted == 0 {
		return false, nil
	}
	if row.AuthorityKind == "tenant" {
		err = queries.DeleteCollectedTenantAuthorityProjection(ctx, foghorndb.DeleteCollectedTenantAuthorityProjectionParams{
			TenantID: row.AuthorityID, AuthorityVersion: row.AuthorityVersion,
		})
	} else {
		err = queries.DeleteCollectedMediaObjectAuthorityProjection(ctx, foghorndb.DeleteCollectedMediaObjectAuthorityProjectionParams{
			AuthorityID: row.AuthorityID, AuthorityVersion: row.AuthorityVersion,
		})
	}
	if err != nil {
		return false, fmt.Errorf("forget media authority projection %s: %w", row.AuthorityID, err)
	}
	if err = tx.Commit(); err != nil {
		return false, fmt.Errorf("commit media authority collection: %w", err)
	}
	return true, nil
}
