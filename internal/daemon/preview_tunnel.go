package daemon

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"time"

	"github.com/example/app-runner/internal/journal"
	"github.com/example/app-runner/internal/previewhost"
	"github.com/example/app-runner/internal/previewtunnel"
)

type previewTunnel interface {
	Run(ctx context.Context) error
	UpdateTLSConfig(cfg *tls.Config)
	ResetPreview(previewID string)
}

type previewTunnelHosts interface {
	PreviewHostByPreviewID(ctx context.Context, previewID string) (journal.PreviewHost, bool, error)
}

type previewTunnelResolver struct {
	hosts   previewTunnelHosts
	ingress previewhost.IngressLocator
	now     func() time.Time
}

func (r previewTunnelResolver) Resolve(ctx context.Context, request previewtunnel.OpenRequest) (string, bool) {
	host, found, err := r.hosts.PreviewHostByPreviewID(ctx, request.PreviewID)
	if err != nil || !found || host.PreviewID != request.PreviewID {
		return "", false
	}
	if subtle.ConstantTimeCompare([]byte(host.PreviewKey), []byte(request.PreviewKey)) != 1 {
		return "", false
	}
	if host.State != journal.PreviewHostDeployed || !host.ExpiresAt.After(r.now()) {
		return "", false
	}
	address, err := r.ingress.IngressAddress(ctx, request.PreviewID)
	if err != nil || address == "" {
		return "", false
	}
	return address, true
}

func (d *Daemon) newPreviewTunnel(tlsCfg *tls.Config) previewTunnel {
	locator, ok := d.previewHostController.(previewhost.IngressLocator)
	if !ok {
		return nil
	}
	url, err := previewtunnel.TunnelURL(d.cfg.ControlPlaneURL)
	if err != nil {
		d.logger.Warn("preview tunnel disabled: control plane url", "err", err)
		return nil
	}
	resolver := previewTunnelResolver{hosts: d.journal, ingress: locator, now: d.previewHostNow}
	return previewtunnel.NewClient(url, tlsCfg, resolver, d.logger.With("component", "preview_tunnel"))
}

func (d *Daemon) startPreviewTunnel(ctx context.Context) {
	if d.previewTunnel == nil || !d.previewTunnelAdvertised.Load() {
		return
	}
	d.previewTunnelMu.Lock()
	defer d.previewTunnelMu.Unlock()
	d.stopPreviewTunnelLocked()
	if ctx.Err() != nil {
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	d.previewTunnelCancel, d.previewTunnelDone = cancel, done
	tunnel := d.previewTunnel
	go func() {
		defer close(done)
		if err := tunnel.Run(runCtx); err != nil && runCtx.Err() == nil {
			d.logger.Warn("preview tunnel stopped", "err", err)
		}
	}()
}

func (d *Daemon) stopPreviewTunnel() {
	d.previewTunnelMu.Lock()
	defer d.previewTunnelMu.Unlock()
	d.stopPreviewTunnelLocked()
}

func (d *Daemon) stopPreviewTunnelLocked() {
	if d.previewTunnelCancel == nil {
		return
	}
	d.previewTunnelCancel()
	<-d.previewTunnelDone
	d.previewTunnelCancel, d.previewTunnelDone = nil, nil
}

func (d *Daemon) updatePreviewTunnelTLS(tlsCfg *tls.Config) {
	if d.previewTunnel != nil {
		d.previewTunnel.UpdateTLSConfig(tlsCfg)
	}
}

func (d *Daemon) resetPreviewTunnel(previewID string) {
	if d.previewTunnel != nil {
		d.previewTunnel.ResetPreview(previewID)
	}
}
