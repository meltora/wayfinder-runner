//go:build rotationinterop

package daemon

import "time"

func (d *Daemon) SetInteropClock(now func() time.Time) { d.now = now }
