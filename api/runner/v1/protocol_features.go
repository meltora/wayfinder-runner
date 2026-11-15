package v1

import "sort"

const (
	ProtocolFeatureJobSpecProviderAuthMode = "job_spec_provider_auth_mode"

	ProviderAuthModeSubscription = "subscription"
	ProviderAuthModeAPIKey       = "api_key"
)

var supportedProtocolFeatures = []string{ProtocolFeatureJobSpecProviderAuthMode}

func SupportedProtocolFeatures() []string {
	features := append([]string(nil), supportedProtocolFeatures...)
	sort.Strings(features)
	return features
}
