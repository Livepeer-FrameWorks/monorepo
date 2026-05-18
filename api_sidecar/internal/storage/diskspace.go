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

func EffectiveDiskSpace(path string, capacityBytes uint64) (*DiskSpace, error) {
	space, err := GetDiskSpaceWalk(path)
	if err != nil {
		return nil, err
	}
	if capacityBytes == 0 {
		return space, nil
	}
	usedBytes, err := DirectorySize(path)
	if err != nil {
		return nil, err
	}
	logicalAvailable := uint64(0)
	if usedBytes < capacityBytes {
		logicalAvailable = capacityBytes - usedBytes
	}
	available := space.AvailableBytes
	if logicalAvailable < available {
		available = logicalAvailable
	}
	total := space.TotalBytes
	if capacityBytes < total {
		total = capacityBytes
	}
	return &DiskSpace{TotalBytes: total, AvailableBytes: available}, nil
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
