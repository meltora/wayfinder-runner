package previewhost

import (
	"context"
	"errors"
	"fmt"
)

const jobPreviewAlias = "our-preview"

var _ JobNetworkAttacher = (*Docker)(nil)

func isolatedNetworkOptions() map[string]string {
	return map[string]string{
		"com.docker.network.bridge.inhibit_ipv4": "true",
		"com.docker.network.enable_ipv6":         "false",
	}
}

func labelsMatch(have, want map[string]string) bool {
	for key, value := range want {
		if have[key] != value {
			return false
		}
	}
	return true
}

func (d *Docker) AttachJob(ctx context.Context, previewID, attemptID, jobContainer string) error {
	if previewID == "" || attemptID == "" || jobContainer == "" {
		return errors.New("job preview network requires a preview id, an attempt id and a job container")
	}
	if !dockerObjectNamePattern.MatchString(jobContainer) && !dockerContainerIDPattern.MatchString(jobContainer) {
		return errors.New("job preview network requires a valid job container name")
	}
	labels := JobNetworkLabels(d.cfg.RunnerID, attemptID)
	if err := d.removeJobNetworks(ctx, labels); err != nil {
		return err
	}
	name := JobNetworkName(d.cfg.RunnerID, attemptID)
	spec := NetworkSpec{Name: name, Labels: labels, Internal: true, Options: isolatedNetworkOptions()}
	if err := d.engine.CreateNetwork(ctx, spec); err != nil {
		return fmt.Errorf("create job preview network: %w", err)
	}
	if err := d.connectJob(ctx, name, previewID, jobContainer); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), d.cfg.TeardownTimeout)
		defer cancel()
		return errors.Join(err, d.removeJobNetwork(cleanupCtx, name))
	}
	return nil
}

func (d *Docker) connectJob(ctx context.Context, network, previewID, jobContainer string) error {
	if err := d.engine.ConnectNetwork(ctx, network, jobContainer, ""); err != nil {
		return fmt.Errorf("attach job container to its preview network: %w", err)
	}
	gateway := gatewayName(ResourcePrefix(d.cfg.RunnerID, previewID))
	if err := d.engine.ConnectNetwork(ctx, network, gateway, jobPreviewAlias); err != nil {
		return fmt.Errorf("attach preview gateway to the job network: %w", err)
	}
	return nil
}

func (d *Docker) DetachJob(ctx context.Context, attemptID string) error {
	if attemptID == "" {
		return errors.New("job preview network removal requires an attempt id")
	}
	detachCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), d.cfg.TeardownTimeout)
	defer cancel()
	return d.removeJobNetworks(detachCtx, JobNetworkLabels(d.cfg.RunnerID, attemptID))
}

func (d *Docker) SweepJobNetworks(ctx context.Context, keep func(attemptID string) bool) error {
	networks, err := d.engine.ListNetworks(ctx, map[string]string{LabelPreviewJobNetwork: d.cfg.RunnerID})
	if err != nil {
		return err
	}
	var errs []error
	for _, network := range networks {
		attemptID := network.Labels[LabelAttemptID]
		if attemptID != "" && keep != nil && keep(attemptID) {
			continue
		}
		if err := d.removeJobNetwork(ctx, network.Name); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (d *Docker) removeJobNetworks(ctx context.Context, labels map[string]string) error {
	networks, err := d.engine.ListNetworks(ctx, labels)
	if err != nil {
		return err
	}
	var errs []error
	for _, network := range networks {
		if err := d.removeJobNetwork(ctx, network.Name); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (d *Docker) removeJobNetwork(ctx context.Context, name string) error {
	containers, orphans, err := d.engine.NetworkContainers(ctx, name)
	if err != nil {
		return err
	}
	for _, container := range append(containers, orphans...) {
		if err := d.engine.DisconnectNetwork(ctx, name, container, true); err != nil {
			return err
		}
	}
	return d.engine.RemoveNetwork(ctx, name)
}
