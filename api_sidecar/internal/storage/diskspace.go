package storage

import (
	"errors"
	"syscall"
)

var ErrInsufficientSpace = errors.New("insufficient disk space")

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

func HasSpaceFor(path string, requiredBytes uint64) error {
	space, err := GetDiskSpace(path)
	if err != nil {
		return err
	}

	headroom := uint64(float64(space.TotalBytes) * 0.05)
	if requiredBytes+headroom > space.AvailableBytes {
		return ErrInsufficientSpace
	}

	return nil
}
