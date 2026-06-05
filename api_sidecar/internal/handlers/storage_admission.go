package handlers

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"frameworks/api_sidecar/internal/admission"
	"frameworks/api_sidecar/internal/leases"
	"frameworks/api_sidecar/internal/storage"
)

// Two-tier disk-write admission control.
//
// Tier 1 (proactive, non-blocking): when the write fits now but the projected
// post-write usage would cross softCleanupThreshold, kick off a background
// cleanup pass and return immediately. The write does not wait for cleanup.
//
// Tier 2 (blocking): when free < expected size, run cleanup synchronously
// with a tight target. If still short after cleanup, return
// storage.ErrInsufficientSpace so callers can fail typed and let Foghorn
// retry on another node.
//
// Edges run hot by design: background cleanup does NOT trim back to
// targetThreshold. It frees just enough to keep room for the next write or
// two, then stops.

// Admission types live in the admission package so the relay can consume the
// typed decision contract without an import cycle through handlers. The
// StorageManager.Decide method below implements admission.Admitter.

// criticalRelayThresholdDefault is the disk-used ratio above which playback
// relay fills flip to CacheMemoryOnly. Below this, the existing two-tier
// admission engine handles soft/hard pressure.
const criticalRelayThresholdDefault = 0.95

// Decide is the single typed admission entrypoint. Behavior per intent is
// documented on the StorageIntent constants. Routes through the same
// admitDiskWrite/ensureRoomForDiskWrite machinery for intents that require
// disk, and short-circuits to CacheMemoryOnly/CacheReject for intents that
// can or must avoid disk.
func (sm *StorageManager) Decide(
	ctx context.Context, dir string, intent admission.StorageIntent, sizeBytes uint64,
) (admission.CacheDecision, error) {
	switch intent {
	case admission.IntentProcessingInput:
		if err := sm.admitDiskWrite(ctx, dir, sizeBytes); err != nil {
			return admission.CacheReject, err
		}
		return admission.CacheToDisk, nil

	case admission.IntentDVRRecording, admission.IntentProcessingOutput, admission.IntentProcessingSourceStage,
		admission.IntentUnsafeImportStage, admission.IntentDVRChapterFinalization:
		if err := sm.admitDiskWrite(ctx, dir, sizeBytes); err != nil {
			return admission.CacheReject, err
		}
		return admission.CacheToDisk, nil

	case admission.IntentPlaybackCache:
		// Walk to the nearest existing ancestor: the block-cache leaf
		// dir (<asset>.blocks) doesn't exist until first fill, so a raw
		// Statfs on it would ENOENT for cold assets. The storage root
		// always exists, so the parent-walk delivers real filesystem
		// stats without pre-creating the leaf.
		space, err := storage.GetDiskSpaceWalk(dir)
		if err != nil {
			return admission.CacheMemoryOnly, nil //nolint:nilerr // safe fallback
		}
		if space.TotalBytes > 0 {
			used := space.TotalBytes - space.AvailableBytes
			if float64(used)/float64(space.TotalBytes) >= criticalRelayThresholdDefault {
				return admission.CacheMemoryOnly, nil
			}
			if sizeBytes > 0 && sizeBytes > space.AvailableBytes {
				if leases.IsDestructiveCleanupAllowed() {
					if cleanupErr := sm.ensureRoomForDiskWrite(ctx, dir, sizeBytes); cleanupErr == nil {
						return admission.CacheToDisk, nil
					}
				}
				return admission.CacheMemoryOnly, nil
			}
		}
		if admitErr := sm.admitDiskWrite(ctx, dir, sizeBytes); admitErr != nil {
			// Playback cache is opportunistic — failed admission means we
			// still serve, just without writing through to disk.
			return admission.CacheMemoryOnly, nil //nolint:nilerr // intentional graceful degradation
		}
		return admission.CacheToDisk, nil

	case admission.IntentWarmCache:
		space, err := storage.GetDiskSpace(dir)
		if err != nil {
			// Warm cache is purely opportunistic; abort cleanly on any
			// pressure signal including stat failure.
			return admission.CacheReject, nil //nolint:nilerr // opportunistic skip
		}
		if space.TotalBytes > 0 {
			used := space.TotalBytes - space.AvailableBytes
			if sm.softCleanupThreshold > 0 &&
				float64(used)/float64(space.TotalBytes) >= sm.softCleanupThreshold {
				return admission.CacheReject, nil
			}
			if sizeBytes > 0 && sizeBytes > space.AvailableBytes {
				return admission.CacheReject, nil
			}
		}
		return admission.CacheToDisk, nil

	default:
		return admission.CacheReject, fmt.Errorf("unknown storage intent %q", intent)
	}
}

// backgroundCleanupRunning is a single-runner sentinel. While set, repeated
// kickoff calls are no-ops. Stored as atomic so the read path stays
// lock-free.
var backgroundCleanupRunning atomic.Bool

// admitDiskWrite gates a disk write. Returns nil to allow the write to
// proceed; returns storage.ErrInsufficientSpace when no amount of cleanup can
// make room.
func (sm *StorageManager) admitDiskWrite(ctx context.Context, dir string, sizeBytes uint64) error {
	if sizeBytes == 0 {
		// Unknown size: skip preflight admission. The write itself will
		// hit ENOSPC if the disk is too tight; that path remains the
		// only signal when callers can't predict the on-disk footprint.
		return nil
	}

	space, err := storage.EffectiveDiskSpace(dir, sm.capacity)
	if err != nil {
		// Path may not exist yet. Fall back to HasSpaceFor's stat-parent walk.
		if err := storage.HasSpaceForWithinCapacity(dir, sizeBytes, sm.capacity); err != nil {
			if errors.Is(err, storage.ErrInsufficientSpace) {
				return sm.ensureRoomForDiskWrite(ctx, dir, sizeBytes)
			}
			return fmt.Errorf("admitDiskWrite statfs: %w", err)
		}
		return nil
	}

	// Tier 2 — blocking: no room right now.
	if space.AvailableBytes < sizeBytes {
		return sm.ensureRoomForDiskWrite(ctx, dir, sizeBytes)
	}

	// Tier 1 — proactive: room exists, but projected usage may cross the
	// soft threshold. Kick off background cleanup if so, and proceed.
	if sm.softCleanupThreshold > 0 && space.TotalBytes > 0 {
		usedAfter := (space.TotalBytes - space.AvailableBytes) + sizeBytes
		usageAfter := float64(usedAfter) / float64(space.TotalBytes)
		if usageAfter > sm.softCleanupThreshold {
			sm.kickoffBackgroundCleanup(sizeBytes)
		}
	}
	return nil
}

// ensureRoomForDiskWrite is the Tier-2 synchronous path. Called when the disk
// already has less free space than sizeBytes. Runs fallbackCleanup with an
// explicit byte target (sizeBytes + the storage reserve), then re-checks.
// Returns storage.ErrInsufficientSpace on failure.
func (sm *StorageManager) ensureRoomForDiskWrite(ctx context.Context, dir string, sizeBytes uint64) error {
	if !leases.IsDestructiveCleanupAllowed() {
		// Boot pause: cannot safely evict yet. Fail fast so Foghorn picks a
		// different node.
		return fmt.Errorf("%w: destructive cleanup paused", storage.ErrInsufficientSpace)
	}

	space, err := storage.EffectiveDiskSpace(dir, sm.capacity)
	if err != nil {
		return fmt.Errorf("ensureRoomForDiskWrite statfs: %w", err)
	}
	needed := storage.RequiredAvailableBytes(sizeBytes)
	if needed <= space.AvailableBytes {
		return nil
	}
	bytesToFree := needed - space.AvailableBytes

	if cleanupErr := sm.fallbackCleanupWithTarget(dir, bytesToFree); cleanupErr != nil {
		// Cleanup itself failed; propagate.
		return fmt.Errorf("ensureRoomForDiskWrite cleanup: %w", cleanupErr)
	}

	// Re-check.
	space, err = storage.EffectiveDiskSpace(dir, sm.capacity)
	if err != nil {
		return fmt.Errorf("ensureRoomForDiskWrite recheck statfs: %w", err)
	}
	if space.AvailableBytes < sizeBytes {
		return fmt.Errorf("%w: free=%dB need=%dB after cleanup", storage.ErrInsufficientSpace, space.AvailableBytes, sizeBytes)
	}
	return nil
}

// kickoffBackgroundCleanup starts a non-aggressive cleanup pass in a
// goroutine. Single-runner: repeated calls while one is running are no-ops.
// Target is "free ~sizeBytes*2 OR be back under softCleanupThreshold."
func (sm *StorageManager) kickoffBackgroundCleanup(sizeBytes uint64) {
	if !backgroundCleanupRunning.CompareAndSwap(false, true) {
		return // already running
	}
	go func() {
		defer backgroundCleanupRunning.Store(false)

		if !leases.IsDestructiveCleanupAllowed() {
			sm.logger.Debug("Background cleanup skipped: destructive cleanup paused")
			return
		}

		dir := sm.basePath
		space, err := storage.GetDiskSpace(dir)
		if err != nil {
			sm.logger.WithError(err).Warn("Background cleanup: statfs failed")
			return
		}

		// Target: free up to 2× the write size, but stop earlier when we're
		// already under softCleanupThreshold. The point is to set up room
		// for the next write, not aggressively trim the disk.
		target := sizeBytes * 2
		if target == 0 {
			target = uint64(float64(space.TotalBytes) * 0.05) // 5% of total as a safety floor
		}

		// Compute how much we'd need to free to also be under softCleanupThreshold.
		softFloor := uint64(float64(space.TotalBytes) * sm.softCleanupThreshold)
		used := space.TotalBytes - space.AvailableBytes
		var bytesToReachSoft uint64
		if used > softFloor {
			bytesToReachSoft = used - softFloor
		}
		// Smaller of the two: prefer the gentler target.
		bytesToFree := target
		if bytesToReachSoft > 0 && bytesToReachSoft < bytesToFree {
			bytesToFree = bytesToReachSoft
		}
		if bytesToFree == 0 {
			return
		}

		sm.logger.WithField("bytes_to_free", bytesToFree).Info("Background cleanup starting (proactive)")
		if err := sm.fallbackCleanupWithTarget(dir, bytesToFree); err != nil {
			sm.logger.WithError(err).Warn("Background cleanup failed")
			return
		}
		sm.logger.Info("Background cleanup completed")
	}()
}
