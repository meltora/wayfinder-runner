package doctor

import (
	"fmt"

	"github.com/example/app-runner/internal/agentpack/trustroot"
	"github.com/example/app-runner/internal/agentpack/trustset"
)

const platformTrustCheckName = "platform-trust"

func platformTrustFailure(reason string) Check {
	return Check{Name: platformTrustCheckName, Status: Fail, Detail: "Platform trust is not current: " + reason + "; upgrade the runner"}
}

func platformTrustCurrent(sequence int64, source string) Check {
	return Check{Name: platformTrustCheckName, Status: OK, Detail: fmt.Sprintf("Platform trust current: the %s signed key set (sequence %d) verifies against this build's trust roots", source, sequence)}
}

func platformTrustCheck(opts Options, deps Dependencies) Check {
	if deps.PlatformTrustRoots == nil || opts.StateDir == "" {
		return Check{Name: platformTrustCheckName, Status: Warn, Detail: "Platform trust was not evaluated"}
	}
	roots, err := deps.PlatformTrustRoots()
	if err != nil {
		return platformTrustFailure("this build's trust roots could not be loaded (" + err.Error() + ")")
	}
	if len(roots.Keys) == 0 {
		return platformTrustFailure("this build embeds no trust roots")
	}
	store, err := trustset.OpenStore(opts.StateDir, roots)
	if err != nil {
		return platformTrustFailure("the stored trust state is unreadable or corrupt")
	}
	snapshot := store.Snapshot()
	if snapshot.Current != nil {
		return platformTrustCurrent(snapshot.Current.Document.Sequence, "applied")
	}
	if snapshot.PersistedSetUnverifiable {
		return platformTrustFailure("the stored signed key set does not verify against this build's trust roots")
	}
	if opts.PlatformTrustedKeySet != nil {
		verified, err := trustset.Verify(*opts.PlatformTrustedKeySet, roots)
		if err != nil {
			return platformTrustFailure("the delivered signed key set does not verify against this build's trust roots")
		}
		return platformTrustCurrent(verified.Document.Sequence, "delivered")
	}
	return Check{Name: platformTrustCheckName, Status: Warn, Detail: "Platform trust not yet delivered: no signed key set has reached this runner"}
}

func defaultPlatformTrustRoots(opts Options, overridePath string) func() (trustroot.Roots, error) {
	return func() (trustroot.Roots, error) {
		return trustroot.Resolve(opts.BuildChannel, overridePath)
	}
}
