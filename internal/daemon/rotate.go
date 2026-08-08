package daemon

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/example/app-runner/internal/enroll"
	"github.com/example/app-runner/internal/identity"
	"github.com/example/app-runner/internal/transport"
)

func (d *Daemon) rotateCert(ctx context.Context) error {
	d.rotationMu.Lock()
	defer d.rotationMu.Unlock()
	oldCertPEM, caPEM, err := d.store.LoadCert()
	if err != nil {
		return fmt.Errorf("load current cert: %w", err)
	}
	oldKey, err := d.store.LoadKey()
	if err != nil {
		return fmt.Errorf("load current key: %w", err)
	}
	oldKeyDER, err := marshalKey(oldKey)
	if err != nil {
		return err
	}
	currentTLS, err := transport.TLSConfigFromPEM(oldCertPEM, oldKeyDER, caPEM)
	if err != nil {
		return fmt.Errorf("build current mTLS identity: %w", err)
	}
	newKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	csr, err := identity.NewCSR(newKey, d.runnerID)
	if err != nil {
		return err
	}
	httpURL := d.cfg.ControlPlaneHTTPURL
	if httpURL == "" {
		httpURL = deriveHTTPURL(d.cfg.ControlPlaneURL)
	}
	parsed, err := url.Parse(httpURL)
	if err != nil || parsed.Scheme != "https" {
		return fmt.Errorf("certificate rotation requires an HTTPS control-plane endpoint")
	}
	client := &enroll.Client{
		BaseURL:    httpURL,
		HTTPClient: &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: currentTLS}},
	}
	resp, err := client.Rotate(ctx, enroll.RotateRequest{
		CSR:           string(csr),
		RunnerVersion: Version,
	})
	if err != nil {
		return err
	}
	expectedRunnerID := d.runnerID
	if resp.RunnerID != expectedRunnerID {
		return fmt.Errorf("rotation response runner ID %q does not match %q", resp.RunnerID, expectedRunnerID)
	}
	if err := identity.ValidateIssued([]byte(resp.Certificate), []byte(resp.CACert), newKey, expectedRunnerID, time.Now()); err != nil {
		return fmt.Errorf("validate rotated identity: %w", err)
	}
	if err := d.store.SaveIdentity(newKey, []byte(resp.Certificate), []byte(resp.CACert)); err != nil {
		return fmt.Errorf("commit rotated identity: %w", err)
	}
	keyDER, err := marshalKey(newKey)
	if err != nil {
		return err
	}
	tlsCfg, err := transport.TLSConfigFromPEM([]byte(resp.Certificate), keyDER, []byte(resp.CACert))
	if err != nil {
		return err
	}
	if d.client != nil {
		d.client.UpdateTLSConfig(tlsCfg)
	}
	d.setControlTLS(tlsCfg)
	d.logger.Info("certificate rotated", "runner_id", resp.RunnerID)
	return nil
}

func (d *Daemon) rotationLoop(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			should, err := d.store.ShouldRotate(time.Now())
			if err != nil {
				continue
			}
			if !should {
				continue
			}
			rotateCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			if err := d.rotateCert(rotateCtx); err != nil {
				d.logger.Error("certificate rotation failed", "err", err)
			}
			cancel()
		}
	}
}
