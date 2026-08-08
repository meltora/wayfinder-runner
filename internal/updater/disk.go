package updater

import (
	"fmt"
	"golang.org/x/sys/unix"
)

type systemDiskSpace struct{}

func (systemDiskSpace) FreeBytes(path string) (uint64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, err
	}
	if stat.Bsize <= 0 {
		return 0, fmt.Errorf("invalid filesystem block size")
	}
	blocks, size := uint64(stat.Bavail), uint64(stat.Bsize)
	if blocks > ^uint64(0)/size {
		return ^uint64(0), nil
	}
	return blocks * size, nil
}
func (c *Coordinator) checkDisk(r Release) error {
	if r.Size <= 0 || c.cfg.MaxExtractedBytes <= 0 {
		return fmt.Errorf("update disk requirement must be positive")
	}
	reserve := uint64(64 << 20)
	// #nosec G115 -- both signed values are explicitly positive above.
	archive, extracted := uint64(r.Size), uint64(c.cfg.MaxExtractedBytes)
	if archive > ^uint64(0)-extracted || archive+extracted > ^uint64(0)-reserve {
		return fmt.Errorf("update disk requirement overflows")
	}
	required := archive + extracted + reserve
	for _, path := range []string{c.cfg.StateDir, c.cfg.VersionsDir} {
		free, err := c.disk.FreeBytes(path)
		if err != nil {
			return fmt.Errorf("check free space: %w", err)
		}
		if free < required {
			return fmt.Errorf("insufficient disk space for update")
		}
	}
	return nil
}
