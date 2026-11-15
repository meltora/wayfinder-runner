package validation

import (
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func TestJobSpecAcceptsOnlyKnownProviderAuthModes(t *testing.T) {
	for _, mode := range []string{"", v1.ProviderAuthModeSubscription, v1.ProviderAuthModeAPIKey} {
		job := validJob()
		job.ProviderAuthMode = mode
		if err := JobSpec(job); err != nil {
			t.Fatalf("JobSpec(%q) error = %v", mode, err)
		}
	}
	for _, mode := range []string{"API_KEY", "oauth", " api_key"} {
		job := validJob()
		job.ProviderAuthMode = mode
		if err := JobSpec(job); err == nil || !strings.Contains(err.Error(), "provider_auth_mode") {
			t.Fatalf("JobSpec(%q) error = %v, want provider_auth_mode refusal", mode, err)
		}
	}
}
