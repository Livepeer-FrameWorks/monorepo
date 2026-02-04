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

	totalBytes := stat.Blocks * uint64(stat.Bsize)
	availableBytes := stat.Bavail * uint64(stat.Bsize)

	return &DiskSpace{TotalBytes: totalBytes, AvailableBytes: availableBytes}, nil
}

func statfsExistingPath(path string) (*DiskSpace, error) {
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

func HasSpaceFor(path string, requiredBytes uint64) error {
	// Ensure the target directory exists so Statfs has a stable path.
	// This is a no-op if it already exists.
	_ = os.MkdirAll(path, 0755)

	space, err := statfsExistingPath(path)
	if err != nil {
		return fmt.Errorf("statfs failed for %s: %w", path, err)
	}

	// Keep 5% of total disk free as headroom.
	headroom := space.TotalBytes / 20
	needed := requiredBytes
	if needed < MinFreeBytes {
		needed = MinFreeBytes
	}
	if needed+headroom > space.AvailableBytes {
		return fmt.Errorf("%w: need=%dB headroom=%dB available=%dB path=%s", ErrInsufficientSpace, needed, headroom, space.AvailableBytes, path)
	}

	return nil
}

func IsInsufficientSpace(err error) bool {
	return errors.Is(err, ErrInsufficientSpace)
}
