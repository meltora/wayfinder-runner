package daemon

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/example/app-runner/internal/enroll"
	"github.com/example/app-runner/internal/identity"
	"github.com/example/app-runner/internal/transport"
)

const (
	rotationCheckInterval  = 15 * time.Minute
	rotationRequestTimeout = 2 * time.Minute
	rotationSwitchFloor    = 72 * time.Hour
)

const (
	rotationFaultPendingKeySaved  = "pending_key_saved"
	rotationFaultResponseReceived = "response_received"
	rotationFaultCertificateSaved = "certificate_saved"
	rotationFaultPromoted         = "promoted"
	rotationFaultBeforeConfirm    = "before_confirm"
)

func rotationSwitchDue(now, notAfter time.Time, activeLease, immediate bool) bool {
	return immediate || !activeLease || notAfter.Sub(now) < rotationSwitchFloor
}

func (d *Daemon) injectRotationFault(point string) error {
	if d.rotationFault == nil {
		return nil
	}
	return d.rotationFault(point)
}

func (d *Daemon) rotateCert(ctx context.Context) error {
	return d.renewCertificate(ctx, true)
}

func (d *Daemon) renewCertificate(ctx context.Context, immediate bool) error {
	d.rotationMu.Lock()
	defer d.rotationMu.Unlock()
	now := d.now()
	pending, err := d.store.LoadPending()
	if errors.Is(err, os.ErrNotExist) {
		pending = nil
	} else if err != nil {
		d.logger.Error("pending runner identity is unusable; discarding it", "err", err)
		if discardErr := d.store.DiscardPending(); discardErr != nil {
			return fmt.Errorf("discard unusable pending identity: %w", discardErr)
		}
		pending = nil
	}
	if pending.HasCertificate() {
		if err := identity.ValidateIssued(pending.CertPEM, pending.CAPEM, pending.Key, d.runnerID, now); err != nil {
			d.logger.Warn("pending runner certificate is no longer valid; requesting a new one", "err", err)
			if discardErr := d.store.DiscardPending(); discardErr != nil {
				return fmt.Errorf("discard invalid pending identity: %w", discardErr)
			}
			pending = nil
		}
	}
	if !pending.HasCertificate() {
		if !immediate && pending == nil {
			should, err := d.store.ShouldRotate(now)
			if err != nil {
				d.logger.Warn("certificate rotation check failed", "err", err)
				return nil
			}
			if !should {
				return nil
			}
		}
		if _, err := d.obtainCertificate(ctx, pending, now); err != nil {
			return err
		}
	}
	_, notAfter, err := d.store.CertExpiry()
	if err != nil {
		return fmt.Errorf("read current certificate expiry: %w", err)
	}
	if !rotationSwitchDue(now, notAfter, d.leases.Current() != nil, immediate) {
		d.logger.Info("renewed certificate is ready; switching when no lease is active", "runner_id", d.runnerID)
		return nil
	}
	return d.switchToPending()
}

func (d *Daemon) obtainCertificate(ctx context.Context, pending *identity.StoredIdentity, now time.Time) (*identity.StoredIdentity, error) {
	var key *ecdsa.PrivateKey
	if pending != nil {
		key = pending.Key
	} else {
		generated, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, err
		}
		if err := d.store.SavePendingKey(generated); err != nil {
			return nil, fmt.Errorf("persist pending runner key: %w", err)
		}
		key = generated
	}
	if err := d.injectRotationFault(rotationFaultPendingKeySaved); err != nil {
		return nil, err
	}
	csr, err := identity.NewCSR(key, d.runnerID)
	if err != nil {
		return nil, err
	}
	client, err := d.rotationClient()
	if err != nil {
		return nil, err
	}
	resp, err := client.Rotate(ctx, enroll.RotateRequest{CSR: string(csr), RunnerVersion: Version})
	if err != nil {
		var rejected *enroll.RotateRejectedError
		if errors.As(err, &rejected) && rejected.KeyReused() {
			if discardErr := d.store.DiscardPending(); discardErr != nil {
				return nil, fmt.Errorf("discard reused pending key: %w", discardErr)
			}
		}
		return nil, err
	}
	if err := d.injectRotationFault(rotationFaultResponseReceived); err != nil {
		return nil, err
	}
	if resp.RunnerID != d.runnerID {
		_ = d.store.DiscardPending()
		return nil, fmt.Errorf("rotation response runner ID %q does not match %q", resp.RunnerID, d.runnerID)
	}
	if err := identity.ValidateIssued([]byte(resp.Certificate), []byte(resp.CACert), key, d.runnerID, now); err != nil {
		_ = d.store.DiscardPending()
		return nil, fmt.Errorf("validate rotated identity: %w", err)
	}
	if err := d.store.SavePendingCertificate([]byte(resp.Certificate), []byte(resp.CACert)); err != nil {
		return nil, fmt.Errorf("persist pending runner certificate: %w", err)
	}
	if err := d.injectRotationFault(rotationFaultCertificateSaved); err != nil {
		return nil, err
	}
	return &identity.StoredIdentity{Key: key, CertPEM: []byte(resp.Certificate), CAPEM: []byte(resp.CACert)}, nil
}

func (d *Daemon) rotationClient() (*enroll.Client, error) {
	currentTLS, err := d.currentTLSConfig()
	if err != nil {
		return nil, fmt.Errorf("build current mTLS identity: %w", err)
	}
	httpURL := d.cfg.ControlPlaneHTTPURL
	if httpURL == "" {
		httpURL = deriveHTTPURL(d.cfg.ControlPlaneURL)
	}
	parsed, err := url.Parse(httpURL)
	if err != nil || parsed.Scheme != "https" {
		return nil, fmt.Errorf("certificate rotation requires an HTTPS control-plane endpoint")
	}
	return &enroll.Client{
		BaseURL:    httpURL,
		HTTPClient: &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: currentTLS}},
	}, nil
}

func (d *Daemon) currentTLSConfig() (*tls.Config, error) {
	certPEM, caPEM, err := d.store.LoadCert()
	if err != nil {
		return nil, err
	}
	key, err := d.store.LoadKey()
	if err != nil {
		return nil, err
	}
	keyDER, err := marshalKey(key)
	if err != nil {
		return nil, err
	}
	return transport.TLSConfigFromPEM(certPEM, keyDER, caPEM)
}

func (d *Daemon) switchToPending() error {
	if err := d.store.PromotePending(); err != nil {
		return fmt.Errorf("promote renewed identity: %w", err)
	}
	tlsCfg, err := d.currentTLSConfig()
	if err != nil {
		return fmt.Errorf("load renewed identity: %w", err)
	}
	d.promotedTLS.Store(tlsCfg)
	if err := d.injectRotationFault(rotationFaultPromoted); err != nil {
		return err
	}
	if d.client != nil {
		d.client.UpdateTLSConfig(tlsCfg)
	}
	d.updatePreviewTunnelTLS(tlsCfg)
	d.setControlTLS(tlsCfg)
	d.logger.Info("certificate rotated; reconnecting with the renewed certificate", "runner_id", d.runnerID)
	return nil
}

func (d *Daemon) resumePromotedIdentity(tlsCfg *tls.Config) {
	awaiting, err := d.store.AwaitingConfirmation()
	if err != nil {
		d.logger.Warn("inspect previous runner identity", "err", err)
		return
	}
	if awaiting {
		d.promotedTLS.Store(tlsCfg)
	}
}

func (d *Daemon) confirmRotatedIdentity() {
	promoted := d.promotedTLS.Load()
	if promoted == nil || d.client == nil || d.client.ConnectedTLSConfig() != promoted {
		return
	}
	d.rotationMu.Lock()
	defer d.rotationMu.Unlock()
	if d.promotedTLS.Load() != promoted {
		return
	}
	if err := d.injectRotationFault(rotationFaultBeforeConfirm); err != nil {
		return
	}
	if err := d.store.ConfirmPromoted(); err != nil {
		d.logger.Warn("remove previous runner identity", "err", err)
		return
	}
	d.promotedTLS.CompareAndSwap(promoted, nil)
	d.logger.Info("renewed certificate accepted by the control plane", "runner_id", d.runnerID)
}

func (d *Daemon) fallBackFromRejectedRotation(err error) {
	promoted := d.promotedTLS.Load()
	if promoted == nil || !transport.IsUpgradeUnauthorized(err) {
		return
	}
	d.rotationMu.Lock()
	defer d.rotationMu.Unlock()
	if d.promotedTLS.Load() != promoted {
		return
	}
	d.promotedTLS.Store(nil)
	previous, loadErr := d.store.LoadPrevious()
	if loadErr != nil {
		d.logger.Error("renewed certificate was refused and no previous certificate is available", "err", loadErr)
		return
	}
	if validateErr := identity.ValidateIssued(previous.CertPEM, previous.CAPEM, previous.Key, d.runnerID, d.now()); validateErr != nil {
		d.logger.Error("renewed certificate was refused and the previous certificate is no longer valid", "err", validateErr)
		return
	}
	if restoreErr := d.store.RestorePrevious(); restoreErr != nil {
		d.logger.Error("restore previous runner identity", "err", restoreErr)
		return
	}
	tlsCfg, tlsErr := d.currentTLSConfig()
	if tlsErr != nil {
		d.logger.Error("load previous runner identity", "err", tlsErr)
		return
	}
	if d.client != nil {
		d.client.UpdateTLSConfig(tlsCfg)
	}
	d.updatePreviewTunnelTLS(tlsCfg)
	d.setControlTLS(tlsCfg)
	d.logger.Warn("certificate rotation was not accepted; continuing with the previous certificate", "runner_id", d.runnerID)
}

func (d *Daemon) startRequestedRotation(ctx context.Context) {
	d.rotationCommands.Add(1)
	go func() {
		defer d.rotationCommands.Done()
		d.waitForCommandAcknowledgement()
		rotateCtx, cancel := context.WithTimeout(ctx, rotationRequestTimeout)
		defer cancel()
		if err := d.rotateCert(rotateCtx); err != nil {
			d.logger.Error("requested certificate rotation failed", "err", err)
		}
	}()
}

func (d *Daemon) waitForCommandAcknowledgement() {
	d.commandMu.Lock()
	defer d.commandMu.Unlock()
}

func (d *Daemon) rotationLoop(ctx context.Context) {
	ticker := time.NewTicker(rotationCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rotateCtx, cancel := context.WithTimeout(ctx, rotationRequestTimeout)
			if err := d.renewCertificate(rotateCtx, false); err != nil {
				d.logger.Error("certificate rotation failed", "err", err)
			}
			cancel()
		}
	}
}

func (d *Daemon) startupRotation(ctx context.Context) error {
	rotateCtx, cancel := context.WithTimeout(ctx, rotationRequestTimeout)
	err := d.renewCertificate(rotateCtx, false)
	cancel()
	if err == nil {
		return nil
	}
	if _, notAfter, expiryErr := d.store.CertExpiry(); expiryErr != nil || !d.now().Before(notAfter) {
		return fmt.Errorf("startup certificate rotation: %w", err)
	}
	d.logger.Error("startup certificate rotation failed; continuing with the current certificate", "err", err)
	return nil
}
