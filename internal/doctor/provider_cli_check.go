package doctor

import (
	"context"
	"fmt"
	"strings"

	"github.com/example/app-runner/internal/isolation"
	"github.com/example/app-runner/internal/provider"
)

var probeProviderCLI = isolation.ProbeProviderCLI

type providerCLITarget struct {
	name   string
	binary string
}

func providerCLITargets(opts Options) []providerCLITarget {
	targets := []providerCLITarget{{name: opts.Provider, binary: opts.ProviderBinary}}
	if opts.WorkIntakeProposalEnabled && opts.WorkIntakeProposalAdapter != "" && opts.WorkIntakeProposalAdapter != opts.Provider {
		targets = append(targets, providerCLITarget{name: opts.WorkIntakeProposalAdapter, binary: opts.WorkIntakeProposalBinary})
	}
	return targets
}

func checkProviderCLI(ctx context.Context, opts Options, mapping isolation.UserMapping, mappingErr error) Check {
	c := Check{Name: "job-image-provider-cli"}
	if opts.Isolation == "" || opts.JobImage == "" || opts.Provider == "" {
		c.Status, c.Detail = Warn, "provider CLI in the job image not tested; run doctor --config FILE before production use"
		return c
	}
	if opts.Isolation != "docker" {
		c.Status, c.Detail = Warn, "direct isolation is development-only"
		return c
	}
	if mappingErr != nil {
		c.Status, c.Detail = Fail, "job user mapping: "+mappingErr.Error()
		return c
	}
	targets := providerCLITargets(opts)
	details := make([]string, 0, len(targets))
	for _, target := range targets {
		status, detail := probeTargetProviderCLI(ctx, opts.JobImage, target, mapping)
		if status > c.Status {
			c.Status = status
		}
		if len(targets) > 1 {
			detail = target.name + ": " + detail
		}
		details = append(details, detail)
	}
	c.Detail = strings.Join(details, "; ")
	return c
}

func probeTargetProviderCLI(ctx context.Context, image string, target providerCLITarget, mapping isolation.UserMapping) (Status, string) {
	adapter, err := provider.For(target.name)
	if err != nil {
		return Fail, err.Error()
	}
	contract, ok := adapter.(provider.CLIOptionContract)
	if !ok {
		return Warn, "no runner option contract is declared for " + target.name + "; its CLI options were not probed"
	}
	binary := target.binary
	if binary == "" {
		binary = target.name
	}
	result, err := probeProviderCLI(ctx, image, binary, "doctor", mapping)
	if err != nil {
		return Fail, err.Error()
	}
	required := contract.RequiredCLIOptions()
	if missing := provider.MissingCLIOptions(result.Help, required); len(missing) > 0 {
		return Fail, result.Version + " in the job image does not accept the runner options " + strings.Join(missing, ", ")
	}
	return OK, fmt.Sprintf("%s in the job image accepts all %d runner options", result.Version, len(required))
}
