package storage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

var ErrInsufficientSpace = errors.New("insufficient disk space")

const MinFreeBytes = 1 << 30

type DiskSpace struct {
	TotalBytes     uint64
	AvailableBytes uint64
}

func GetDiskSpace(path string) (*DiskSpace, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return nil, err
	}

	blockSize := statfsBlockSize(stat)
	totalBytes := stat.Blocks * blockSize
	availableBytes := stat.Bavail * blockSize

	return &DiskSpace{TotalBytes: totalBytes, AvailableBytes: availableBytes}, nil
}

// GetDiskSpaceWalk returns disk space for the nearest existing
// ancestor of path. Cold cache directories don't exist yet at
// admission time, but their parent (the storage root) always does —
// statfs on the parent gives the right filesystem stats without
// pre-creating the leaf dir.
func GetDiskSpaceWalk(path string) (*DiskSpace, error) {
	p := path
	for {
		space, err := GetDiskSpace(p)
		if err == nil {
			return space, nil
		}
		// If the path doesn't exist, try its parent.
		if errors.Is(err, syscall.ENOENT) {
			parent := filepath.Dir(p)
			if parent == p {
				return nil, err
			}
			p = parent
			continue
		}
		return nil, err
	}
}

// SystemReserveBytes is the space media storage never plans to use on its
// filesystem, whatever the disk size. Edge media shares the root filesystem
// with the journal (journald is capped at 2 GiB), container logs, the trigger
// WAL and control outbox (one file per trigger, so a control-plane outage on
// a busy node grows them by gigabytes), Mist and Helmsman state, and the
// layers of the next edge image pulled beside the running one during an
// upgrade (several GiB for the accelerator variants). 10 GiB covers those
// together; a percentage threshold alone leaves a small disk a few hundred
// MiB for all of them.
const SystemReserveBytes uint64 = 10 << 30

// statDiskSpace reads the raw filesystem stats EffectiveDiskSpace starts from.
var statDiskSpace = GetDiskSpaceWalk

// EffectiveDiskSpace returns the space media storage may use at path: the
// filesystem minus SystemReserveBytes, further capped by capacityBytes when
// it is non-zero. Freeze, eviction and admission thresholds are fractions of
// the returned total, so they are computed against capacity minus reserve.
func EffectiveDiskSpace(path string, capacityBytes uint64) (*DiskSpace, error) {
	space, err := statDiskSpace(path)
	if err != nil {
		return nil, err
	}
	var usedBytes uint64
	if capacityBytes > 0 {
		usedBytes, err = DirectorySize(path)
		if err != nil {
			return nil, err
		}
	}
	effective := MediaDiskSpace(*space, SystemReserveBytes, capacityBytes, usedBytes)
	return &effective, nil
}

// MediaDiskSpace applies the system reserve to raw filesystem stats and then
// the optional logical capacity cap, where dirUsedBytes is what the media
// directory already holds. A filesystem no larger than the reserve reports
// zero total and zero available.
func MediaDiskSpace(raw DiskSpace, reserve, capacityBytes, dirUsedBytes uint64) DiskSpace {
	total := saturatingSub(raw.TotalBytes, reserve)
	available := min(saturatingSub(raw.AvailableBytes, reserve), total)
	if capacityBytes == 0 {
		return DiskSpace{TotalBytes: total, AvailableBytes: available}
	}
	available = min(available, saturatingSub(capacityBytes, dirUsedBytes))
	total = min(total, capacityBytes)
	return DiskSpace{TotalBytes: total, AvailableBytes: available}
}

// UsageFraction is the used share of the media space. A zero total (a
// filesystem no larger than the reserve) counts as full.
func (d DiskSpace) UsageFraction() float64 {
	if d.TotalBytes == 0 {
		return 1
	}
	return float64(d.TotalBytes-min(d.AvailableBytes, d.TotalBytes)) / float64(d.TotalBytes)
}

func saturatingSub(a, b uint64) uint64 {
	if a <= b {
		return 0
	}
	return a - b
}

func DirectorySize(path string) (uint64, error) {
	var size uint64
	err := filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if info != nil && !info.IsDir() {
			size += uint64(info.Size())
		}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	return size, err
}

func HasSpaceFor(path string, requiredBytes uint64) error {
	return HasSpaceForWithinCapacity(path, requiredBytes, 0)
}

func HasSpaceForWithinCapacity(path string, requiredBytes uint64, capacityBytes uint64) error {
	// Ensure the target directory exists so Statfs has a stable path.
	// This is a no-op if it already exists.
	_ = os.MkdirAll(path, 0755)

	space, err := EffectiveDiskSpace(path, capacityBytes)
	if err != nil {
		return fmt.Errorf("statfs failed for %s: %w", path, err)
	}

	needed := RequiredAvailableBytes(requiredBytes)
	if needed > space.AvailableBytes {
		return fmt.Errorf("%w: need=%dB reserve=%dB available=%dB path=%s", ErrInsufficientSpace, requiredBytes, MinFreeBytes, space.AvailableBytes, path)
	}

	return nil
}

func RequiredAvailableBytes(requiredBytes uint64) uint64 {
	if requiredBytes > ^uint64(0)-MinFreeBytes {
		return ^uint64(0)
	}
	return requiredBytes + MinFreeBytes
}

func IsInsufficientSpace(err error) bool {
	return errors.Is(err, ErrInsufficientSpace)
}
