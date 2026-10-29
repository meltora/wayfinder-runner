package capability

import (
	"math"
	"testing"
)

func TestDiskFreeBytesGuardsTheBlockSizeAndSaturates(t *testing.T) {
	cases := []struct {
		name      string
		available uint64
		blockSize int64
		want      uint64
	}{
		{name: "normal", available: 10, blockSize: 4096, want: 40960},
		{name: "zero block size", available: 10, blockSize: 0, want: 0},
		{name: "negative block size", available: 10, blockSize: -4096, want: 0},
		{name: "overflow", available: math.MaxUint64 / 2, blockSize: 4, want: math.MaxUint64},
		{name: "no blocks", available: 0, blockSize: 4096, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := diskFreeBytes(tc.available, tc.blockSize); got != tc.want {
				t.Fatalf("diskFreeBytes(%d, %d) = %d, want %d", tc.available, tc.blockSize, got, tc.want)
			}
		})
	}
}

func TestDiskFreeBytesAcceptsAnUnsignedBlockSize(t *testing.T) {
	if got := diskFreeBytes(uint64(3), uint32(512)); got != 1536 {
		t.Fatalf("diskFreeBytes = %d", got)
	}
	if got := diskFreeBytes(uint64(3), uint32(0)); got != 0 {
		t.Fatalf("diskFreeBytes with zero block size = %d", got)
	}
}
