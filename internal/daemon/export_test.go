package daemon

import "context"

func (d *Daemon) TestRotate(ctx context.Context) error { return d.rotateCert(ctx) }
