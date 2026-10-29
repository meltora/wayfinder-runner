package daemon

import (
	"bytes"
	"crypto/rand"
	"errors"
	"testing"
	"testing/iotest"
)

func TestConfigurationReloadJitterIsAUnitIntervalDrawFromCryptoRand(t *testing.T) {
	low, high := false, false
	for range 4096 {
		jitter := configurationReloadJitterSample(rand.Reader)
		if jitter < 0 || jitter >= 1 {
			t.Fatalf("jitter %v is outside [0,1)", jitter)
		}
		low = low || jitter < 0.25
		high = high || jitter > 0.75
	}
	if !low || !high {
		t.Fatalf("jitter draws do not spread across the interval: low=%v high=%v", low, high)
	}
}

func TestConfigurationReloadJitterBounds(t *testing.T) {
	if got := configurationReloadJitterSample(bytes.NewReader(make([]byte, 8))); got != 0 {
		t.Fatalf("zero bytes jitter = %v", got)
	}
	if got := configurationReloadJitterSample(bytes.NewReader(bytes.Repeat([]byte{0xff}, 8))); got >= 1 || got < 0.9999999 {
		t.Fatalf("all-ones jitter = %v", got)
	}
}

func TestConfigurationReloadJitterFallsBackToNoJitterWhenRandomnessFails(t *testing.T) {
	if got := configurationReloadJitterSample(iotest.ErrReader(errors.New("no entropy"))); got != 0.5 {
		t.Fatalf("fallback jitter = %v, want 0.5", got)
	}
	if got := configurationReloadJitterSample(bytes.NewReader([]byte{1, 2, 3})); got != 0.5 {
		t.Fatalf("short read jitter = %v, want 0.5", got)
	}
	if got := configurationReloadDelay(1, 0.5); got != configurationReloadInterval {
		t.Fatalf("no-jitter delay = %s, want %s", got, configurationReloadInterval)
	}
}
