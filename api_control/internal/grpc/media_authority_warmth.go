package grpc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
)

const (
	// An authority is kept in cells and renewed while it has been decided on
	// within this window, or is newer than mediaAuthorityNewWindow. After that
	// it stops being renewed and the copies cells hold run out.
	mediaAuthorityUseWindow = 30 * 24 * time.Hour
	mediaAuthorityNewWindow = 7 * 24 * time.Hour
	// A change to an authority nobody uses is published with the validity its
	// current version already has. With less than this left the publication
	// could not reach a cell before it expired, and that expiry already bounds
	// how long the old content is honoured.
	mediaAuthorityCoolingFloor = 2 * time.Minute
)

type mediaAuthorityWarmthContextKey struct{}

// withMediaAuthorityInUse records, for the publishing transaction, whether the
// authority being compiled is in use. A compile that says nothing is treated as
// in use: only the obligation worker and the fetch path decide otherwise.
func withMediaAuthorityInUse(ctx context.Context, inUse bool) context.Context {
	return context.WithValue(ctx, mediaAuthorityWarmthContextKey{}, inUse)
}

func mediaAuthorityInUse(ctx context.Context) bool {
	inUse, ok := ctx.Value(mediaAuthorityWarmthContextKey{}).(bool)
	return !ok || inUse
}

func mediaAuthorityUseDecided(ctx context.Context) bool {
	_, ok := ctx.Value(mediaAuthorityWarmthContextKey{}).(bool)
	return ok
}

// decideMediaObjectInUse settles, before anything expensive is compiled, whether
// a media object is in use: ingesting now, newer than the new window, decided on
// within the use window, or delivered to a cell that cannot say. It reads the
// database only. For an object that is not in use and has no copy left in any
// cell there is nothing to publish, so skip reports that the compile can stop.
func (s *CommodoreServer) decideMediaObjectInUse(ctx context.Context, queries *commodoredb.Queries, authorityID, tenantID string, age time.Duration, ingesting bool, targets []string) (inUseCtx context.Context, skip bool, err error) {
	if mediaAuthorityUseDecided(ctx) {
		return ctx, false, nil
	}
	now := time.Now().UTC()
	inUse, err := s.mediaObjectWanted(ctx, queries, authorityID, tenantID, age, ingesting, now)
	if err != nil {
		return ctx, false, err
	}
	if !inUse {
		reported, reportErr := mediaObjectReportedByEveryCell(ctx, queries, targets)
		if reportErr != nil {
			return ctx, false, reportErr
		}
		inUse = !reported
	}
	ctx = withMediaAuthorityInUse(ctx, inUse)
	if inUse {
		return ctx, false, nil
	}
	skip, err = mediaAuthorityHasNoCopyLeft(ctx, queries, "media_object", authorityID, now)
	if skip {
		markMediaAuthorityDormant(ctx)
	}
	return ctx, skip, err
}

// decideTenantInUse is decideMediaObjectInUse for a tenant authority. A tenant
// is in use while any of its objects is, which every recorded use of an object
// also records for the tenant, and when it has never been published: that is a
// tenant new to the compiler, not one nobody uses.
func (s *CommodoreServer) decideTenantInUse(ctx context.Context, queries *commodoredb.Queries, tenantID string) (inUseCtx context.Context, skip bool, err error) {
	if mediaAuthorityUseDecided(ctx) {
		return ctx, false, nil
	}
	now := time.Now().UTC()
	_, err = queries.GetCurrentMediaAuthorityPublication(ctx, commodoredb.GetCurrentMediaAuthorityPublicationParams{
		AuthorityKind: "tenant", AuthorityID: tenantID,
	})
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return withMediaAuthorityInUse(ctx, true), false, s.recordMediaAuthorityUse(ctx, "tenant", tenantID, tenantID)
	case err != nil:
		return ctx, false, fmt.Errorf("load current tenant authority publication: %w", err)
	}
	inUse, err := mediaAuthorityUsedRecently(ctx, queries, "tenant", tenantID, now)
	if err != nil {
		return ctx, false, err
	}
	ctx = withMediaAuthorityInUse(ctx, inUse)
	if inUse {
		return ctx, false, nil
	}
	gone, err := mediaAuthorityHasNoCopyLeft(ctx, queries, "tenant", tenantID, now)
	if err != nil || !gone {
		return ctx, false, err
	}
	markMediaAuthorityDormant(ctx)
	return ctx, true, nil
}

// mediaObjectWanted is the part of "in use" that needs nothing but the object:
// ingesting now, newer than the new window, or decided on within the use
// window. An object whose tenant has no authority is compiled only when it is
// wanted; otherwise a change to an object nobody uses would wake its tenant.
func (s *CommodoreServer) mediaObjectWanted(ctx context.Context, queries *commodoredb.Queries, authorityID, tenantID string, age time.Duration, ingesting bool, now time.Time) (bool, error) {
	if ingesting || age < mediaAuthorityNewWindow {
		// Being new or being ingested is use, and this is the only place either is
		// seen: recording it keeps the tenant in use along with the object.
		return true, s.recordMediaAuthorityUse(ctx, "media_object", authorityID, tenantID)
	}
	return mediaAuthorityUsedRecently(ctx, queries, "media_object", authorityID, now)
}

// mediaObjectWithoutTenantAuthority decides what a media-object compile does
// when its tenant has no usable authority.
//
// When the tenant's current version has run out but a cell may still hold a
// valid copy of the object, the change is compiled now, on that tenant version
// (correctOnLapsedTenant). Waiting would leave the old object copy valid in the
// cell, and whatever renews the tenant later, possibly a different object's
// use that changes nothing the object depends on, would pair it with a valid
// tenant again before the object's correction arrives.
//
// Otherwise an object that is wanted waits for the tenant's, which is then
// compiled because the object's use keeps the tenant in use. One nobody wants
// has nothing to publish. A deleted object is terminal and always goes on to
// wait: its tombstone must still reach cells.
func (s *CommodoreServer) mediaObjectWithoutTenantAuthority(ctx context.Context, queries *commodoredb.Queries, authorityID, tenantID string, age time.Duration, ingesting, terminal bool, tenantErr error) (correctOnLapsedTenant bool, err error) {
	if !errors.Is(tenantErr, errTenantAuthorityMissing) {
		return false, tenantErr
	}
	if errors.Is(tenantErr, errTenantAuthorityLapsed) {
		gone, horizonErr := mediaAuthorityHasNoCopyLeft(ctx, queries, "media_object", authorityID, time.Now().UTC())
		if horizonErr != nil {
			return false, horizonErr
		}
		if !gone {
			return true, nil
		}
	}
	if terminal || mediaAuthorityUseDecided(ctx) {
		return false, tenantErr
	}
	wanted, err := s.mediaObjectWanted(ctx, queries, authorityID, tenantID, age, ingesting, time.Now().UTC())
	if err != nil {
		return false, err
	}
	if wanted {
		return false, tenantErr
	}
	markMediaAuthorityDormant(ctx)
	return false, nil
}

// mediaAuthorityHasNoCopyLeft reports an authority no cell can still be holding
// as valid, for long enough to be corrected: no version of it, current or
// earlier, is valid past the cooling floor. The current version alone does not
// decide it, because a shorter-lived replacement can run out while a cell that
// never received it still holds the longer version it replaced.
func mediaAuthorityHasNoCopyLeft(ctx context.Context, queries *commodoredb.Queries, authorityKind, authorityID string, now time.Time) (bool, error) {
	horizon, err := mediaAuthorityValidHorizon(ctx, queries, authorityKind, authorityID)
	if err != nil {
		return false, err
	}
	return !horizon.After(now.Add(mediaAuthorityCoolingFloor)), nil
}

// mediaAuthorityCurrentLapsed reports that the current version of an authority
// is missing or about to run out. That is what a compile building on it needs
// to know; whether some cell still holds an older valid copy is a different
// question, answered by mediaAuthorityHasNoCopyLeft.
func mediaAuthorityCurrentLapsed(ctx context.Context, queries *commodoredb.Queries, authorityKind, authorityID string, now time.Time) (bool, error) {
	current, err := queries.GetCurrentMediaAuthorityPublication(ctx, commodoredb.GetCurrentMediaAuthorityPublicationParams{
		AuthorityKind: authorityKind, AuthorityID: authorityID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("load current %s authority publication: %w", authorityKind, err)
	}
	return !current.ValidUntil.After(now.Add(mediaAuthorityCoolingFloor)), nil
}

// mediaAuthorityValidHorizon is the latest instant any version of an authority
// is valid until; the zero time when none was ever published.
func mediaAuthorityValidHorizon(ctx context.Context, queries *commodoredb.Queries, authorityKind, authorityID string) (time.Time, error) {
	horizon, err := queries.GetMediaAuthorityValidHorizon(ctx, commodoredb.GetMediaAuthorityValidHorizonParams{
		AuthorityKind: authorityKind, AuthorityID: authorityID,
	})
	if err != nil {
		return time.Time{}, fmt.Errorf("load %s authority validity horizon: %w", authorityKind, err)
	}
	return horizon.UTC(), nil
}

type mediaAuthorityLongValidityContextKey struct{}

// withMediaAuthorityLongValidity offers the publishing transaction a validity
// longer than the one it was called with. Which cells the version goes to is
// only known there, and the longer validity is used only when every one of them
// accepts it. Every decision a cell makes also needs the tenant authority, which
// stays short, so a long object validity does not lengthen what a cell serves
// while it cannot reach the control plane.
func withMediaAuthorityLongValidity(ctx context.Context, validUntil time.Time) context.Context {
	return context.WithValue(ctx, mediaAuthorityLongValidityContextKey{}, validUntil)
}

func mediaAuthorityLongValidity(ctx context.Context) time.Time {
	validUntil, _ := ctx.Value(mediaAuthorityLongValidityContextKey{}).(time.Time) //nolint:errcheck // absent means no longer validity was offered
	return validUntil
}

// mediaAuthorityUsedRecently reports whether an authority was decided on within
// the use window. An authority with no recorded use counts as used when use
// started being recorded.
func mediaAuthorityUsedRecently(ctx context.Context, queries *commodoredb.Queries, authorityKind, authorityID string, now time.Time) (bool, error) {
	lastUsedAt, err := queries.GetMediaAuthorityLastUse(ctx, commodoredb.GetMediaAuthorityLastUseParams{
		AuthorityKind: authorityKind, AuthorityID: authorityID,
	})
	if err != nil {
		return false, fmt.Errorf("load %s authority use: %w", authorityKind, err)
	}
	return lastUsedAt.After(now.Add(-mediaAuthorityUseWindow)), nil
}

// recordMediaAuthorityUse advances an authority's use to today and its tenant's
// with it, so a tenant is never colder than its objects, and makes sure the
// renewal of each is not left dormant: the copy cells hold is still valid and
// has to be extended before it runs out, not only after.
//
// The renewal is revived every time, not only when the use row advanced: a use
// that was already recorded today may be exactly the one a dormant settlement
// raced. A renewal that is being compiled at that moment is folded (its revision
// moves), so a compile that has just decided the authority is unused cannot
// settle it dormant afterwards. Use row and revival commit together.
//
// A compile that records use of the authority it is compiling does not fold its
// own lane: its own settlement would then always lose, and it would run again
// forever.
func (s *CommodoreServer) recordMediaAuthorityUse(ctx context.Context, authorityKind, authorityID, tenantID string) error {
	return database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		queries := commodoredb.New(tx)
		record := func(kind, id string) error {
			if _, err := queries.RecordMediaAuthorityUse(ctx, commodoredb.RecordMediaAuthorityUseParams{
				AuthorityKind: kind, AuthorityID: id, TenantID: tenantID,
			}); err != nil {
				return fmt.Errorf("record %s authority use: %w", kind, err)
			}
			if _, err := queries.ReviveDormantMediaAuthorityRenewal(ctx, commodoredb.ReviveDormantMediaAuthorityRenewalParams{
				TargetKey: kind + ":" + id, CompilingLane: mediaAuthorityObligationLane(ctx),
			}); err != nil {
				return fmt.Errorf("revive %s authority renewal: %w", kind, err)
			}
			return nil
		}
		if authorityKind != "tenant" {
			if err := record(authorityKind, authorityID); err != nil {
				return err
			}
		}
		return record("tenant", tenantID)
	})
}

// mediaAuthorityCellsAcceptLongValidity reports whether every one of the cells
// attested that all its replicas accept a media-object authority valid for
// sharedauthority.MaxMediaObjectValidity. A replica that predates it rejects
// the envelope as malformed, so the nominal version validity remains short
// until all recipients support it. Retirement caps may shorten individual copies.
func mediaAuthorityCellsAcceptLongValidity(ctx context.Context, queries *commodoredb.Queries, cells []string) (bool, error) {
	if len(cells) == 0 {
		return true, nil
	}
	capabilities, err := queries.ListMediaCellAuthorityFeatures(ctx, cells)
	if err != nil {
		return false, fmt.Errorf("load cell capabilities: %w", err)
	}
	accepting := make(map[string]bool, len(capabilities))
	for _, capability := range capabilities {
		accepting[capability.CellID] = capability.LongValidityReady
	}
	for _, cell := range cells {
		if !accepting[cell] {
			return false, nil
		}
	}
	return true, nil
}

// mediaAuthorityObjectsCappedByTenant reports whether objects delivered to
// these cells are still held to the tenant authority's validity, which they are
// until every cell accepts the long validity.
func mediaAuthorityObjectsCappedByTenant(ctx context.Context, queries *commodoredb.Queries, cells []string) (bool, error) {
	long, err := mediaAuthorityCellsAcceptLongValidity(ctx, queries, cells)
	return !long, err
}

// mediaObjectReportedByEveryCell reports whether every cell the object is
// delivered to reports use. A cell that does not would let an object in use
// look unused, so such an object stays in use.
func mediaObjectReportedByEveryCell(ctx context.Context, queries *commodoredb.Queries, cells []string) (bool, error) {
	if len(cells) == 0 {
		return true, nil
	}
	capabilities, err := queries.ListMediaCellAuthorityFeatures(ctx, cells)
	if err != nil {
		return false, fmt.Errorf("load cell capabilities: %w", err)
	}
	reporting := make(map[string]bool, len(capabilities))
	for _, capability := range capabilities {
		reporting[capability.CellID] = capability.UseReportsReady
	}
	for _, cell := range cells {
		if !reporting[cell] {
			return false, nil
		}
	}
	return true, nil
}
