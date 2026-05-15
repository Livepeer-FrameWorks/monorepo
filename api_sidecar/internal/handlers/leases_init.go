package handlers

import (
	"context"
	"sync"
	"time"

	"frameworks/api_sidecar/internal/control"
	"frameworks/api_sidecar/internal/leases"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// Tracks the two Mist reconciliation poll classes needed to unpause boot
// destructive cleanup: active_streams (for source leases) and clients (for
// viewer leases). Once both have completed at least one successful pass,
// boot pause lifts (subject to chapter-registry rehydration also being done).
var (
	mistPollMu              sync.Mutex
	mistActiveStreamsPolled bool
	mistClientsPolled       bool
)

func markMistActiveStreamsPolled() {
	mistPollMu.Lock()
	defer mistPollMu.Unlock()
	if mistActiveStreamsPolled {
		return
	}
	mistActiveStreamsPolled = true
	maybeMarkReconcileDoneLocked()
}

func markMistClientsPolled() {
	mistPollMu.Lock()
	defer mistPollMu.Unlock()
	if mistClientsPolled {
		return
	}
	mistClientsPolled = true
	maybeMarkReconcileDoneLocked()
}

func maybeMarkReconcileDoneLocked() {
	if mistActiveStreamsPolled && mistClientsPolled {
		leases.MarkMistReconcileDone()
	}
}

// InitLeases wires the lease tracker, registries, heat tracker, and deferred-
// delete store into process-global singletons consumed by the trigger handlers
// and cleanup paths.
//
// Boot order: this must be called AFTER InitStorageManager so the storage path
// is known; it starts a goroutine that rehydrates the DVR chapter registry
// from disk. Destructive cleanup stays paused until that rehydration finishes
// AND the first successful Mist API reconciliation completes (see poller.go).
func InitLeases(logger logging.Logger, storagePath string) {
	segIndex := control.LocalSegmentIndexInstance(logger)
	heat := leases.NewHeatTracker()
	tracker := leases.NewTracker(segIndex, heat)
	sourceReg := leases.NewSourceRegistry()
	chapterReg := leases.NewChapterRegistry()

	// Deferred-delete store: when an operator delete arrives for a leased
	// asset, the intent persists here and a retry loop drains as leases
	// release. The deleter dispatches back to the existing Delete* helpers,
	// which themselves no longer enqueue when the underlying delete succeeds.
	//
	// onDeleted fires only when the actual file removal succeeds (not when
	// the intent is queued). It is the single point that emits
	// ArtifactDeleted back to Foghorn, ensuring the control-plane state only
	// advances after bytes are really gone.
	deferred := leases.NewDeferredStore(storagePath, func(assetType, assetHash string) (uint64, error) {
		// Drain must honor the boot-pause and lease invariants. The immediate
		// deleters check the lease state internally; this top-level gate adds
		// the boot-pause check so a drain tick during BOOT_PAUSED becomes a
		// no-op instead of bypassing the pause.
		if !leases.IsDestructiveCleanupAllowed() {
			return 0, leases.ErrLeaseHeld
		}
		switch assetType {
		case "clip":
			return Current().deleteClipImmediate(assetHash)
		case "vod":
			return Current().deleteVODImmediate(assetHash)
		case "dvr":
			return Current().deleteDVRImmediate(assetHash)
		}
		return 0, nil
	}, func(p leases.PendingDelete, bytes uint64) {
		artifactType := p.AssetType
		// Foghorn's control-plane lifecycle uses "clip"/"vod"/"dvr" as-is.
		if err := control.SendArtifactDeleted(p.AssetHash, "", "manual_deferred", artifactType, bytes); err != nil {
			logger.WithError(err).WithFields(logging.Fields{
				"asset_type": p.AssetType,
				"asset_hash": p.AssetHash,
			}).Warn("Deferred delete drain: failed to send ArtifactDeleted")
		}
	})
	if err := deferred.Load(); err != nil {
		logger.WithError(err).Warn("Deferred-delete store load failed; continuing with empty queue")
	}

	leases.Install(tracker, sourceReg, chapterReg, heat, deferred)

	// Wire the control package's DropUnsyncedSegment lease guard. Returns
	// true when any source lease pins the (dvr_hash, segment_name), so
	// DropUnsyncedSegment can refuse for disk_pressure callers without
	// importing the leases package directly.
	control.DropLeaseChecker = func(dvrHash, segmentName string) bool {
		if tracker == nil {
			return false
		}
		// Asset-level check covers any chapter lease for the dvr_hash; the
		// segment-level guard inside EvictUploadedSegments already covers
		// per-segment ActiveViews.
		return tracker.IsAssetLeased(leases.AssetKey{Type: "dvr", Hash: dvrHash})
	}

	// Chapter registry rehydration is bounded by on-disk inventory. Done in
	// a goroutine so startup is not blocked, but cleanup stays paused until
	// it completes.
	go func() {
		if err := chapterReg.Rehydrate(storagePath); err != nil {
			logger.WithError(err).Warn("DVR chapter registry rehydration failed; degraded leases may install on first DVR USER_NEW")
		}
		leases.MarkChapterRehydrateDone()
		logger.Info("DVR chapter registry rehydrated")
	}()

	// Periodic deferred-delete drain. Cheap; only does work when the queue
	// is non-empty.
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		ctx := context.Background()
		_ = ctx
		for range ticker.C {
			if deferred.Count() == 0 {
				continue
			}
			if n := deferred.Drain(); n > 0 {
				logger.WithField("drained", n).Info("Deferred delete drain processed entries")
			}
		}
	}()
}
