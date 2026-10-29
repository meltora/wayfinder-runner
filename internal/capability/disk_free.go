package capability

import (
	"math"
	"math/bits"
)

func diskFreeBytes[B ~int32 | ~int64 | ~uint32 | ~uint64](available uint64, blockSize B) uint64 {
	if blockSize <= 0 {
		return 0
	}
	high, low := bits.Mul64(available, uint64(blockSize))
	if high != 0 {
		return math.MaxUint64
	}
	return low
}
