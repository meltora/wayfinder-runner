package daemon

import (
	"io/fs"
	"time"
)

type statKey struct {
	dev   uint64
	ino   uint64
	mode  uint32
	size  int64
	mtime int64
	ctime int64
}

func fileStatKey(info fs.FileInfo) (statKey, bool) {
	if info == nil {
		return statKey{}, false
	}
	return platformStatKey(info)
}

func (k statKey) settledBefore(moment time.Time, window time.Duration) bool {
	limit := moment.Add(-window).UnixNano()
	return k.mtime < limit && k.ctime < limit
}
