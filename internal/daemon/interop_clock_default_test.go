//go:build !rotationinterop

package daemon_test

import (
	"reflect"
	"testing"

	"github.com/example/app-runner/internal/daemon"
)

func TestInteropClockIsAbsentFromDefaultBuilds(t *testing.T) {
	if _, ok := reflect.TypeOf(&daemon.Daemon{}).MethodByName("SetInteropClock"); ok {
		t.Fatal("the interop clock override must only exist with the rotationinterop build tag")
	}
}
