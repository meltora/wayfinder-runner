package v1

const ProviderUsageMaxCount uint64 = 1<<53 - 1

type ProviderUsage struct {
	Provider                 string  `json:"provider"`
	Model                    string  `json:"model,omitempty"`
	InputTokens              uint64  `json:"input_tokens"`
	OutputTokens             uint64  `json:"output_tokens"`
	CacheReadInputTokens     *uint64 `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens *uint64 `json:"cache_creation_input_tokens,omitempty"`
}
