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
		dev: stat.Dev, ino: stat.Ino, mode: stat.Mode, size: stat.Size,
		mtime: stat.Mtim.Nano(), ctime: stat.Ctim.Nano(),
	}, true
}
