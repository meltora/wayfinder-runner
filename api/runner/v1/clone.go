package v1

import (
	"encoding/json"
	"fmt"
)

// CloneJobSpec gives an executing job exclusive ownership of all slices and
// nested objects decoded from the command. This prevents later command-buffer
// reuse from changing immutable execution authority or revision context.
func CloneJobSpec(input JobSpec) (JobSpec, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return JobSpec{}, fmt.Errorf("encode job clone: %w", err)
	}
	var clone JobSpec
	if err := json.Unmarshal(raw, &clone); err != nil {
		return JobSpec{}, fmt.Errorf("decode job clone: %w", err)
	}
	return clone, nil
}
