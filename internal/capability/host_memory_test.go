package capability

import (
	"runtime"
	"testing"
)

func TestTotalMemoryMatchesTheAdvertisedHostMemory(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("host memory is only probed on linux and darwin")
	}
	if TotalMemory() == 0 || TotalMemory() != totalMemory() {
		t.Fatalf("TotalMemory() = %d, totalMemory() = %d", TotalMemory(), totalMemory())
	}
}
