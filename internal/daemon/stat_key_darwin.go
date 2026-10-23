package daemon

import (
	"io/fs"
	"syscall"
)

func platformStatKey(info fs.FileInfo) (statKey, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return statKey{}, false
	}
	return statKey{
		dev: uint64(uint32(stat.Dev)), ino: stat.Ino, mode: uint32(stat.Mode), size: stat.Size,
		mtime: stat.Mtimespec.Nano(), ctime: stat.Ctimespec.Nano(),
	}, true
}
