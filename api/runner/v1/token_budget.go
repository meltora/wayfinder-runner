package v1

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

type TokenBudget struct {
	RunLimitTokens         *uint64 `json:"run_limit_tokens,omitempty"`
	ProjectRemainingTokens *uint64 `json:"project_remaining_tokens,omitempty"`
}

func (budget *TokenBudget) UnmarshalJSON(data []byte) error {
	type wireTokenBudget TokenBudget
	var decoded wireTokenBudget
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return fmt.Errorf("decode token budget: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("decode token budget: trailing JSON")
	}
	*budget = TokenBudget(decoded)
	return nil
}
