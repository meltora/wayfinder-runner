package config

import "fmt"

func ValidateCheck(id string, check CheckConfig) error {
	if len(id) > 128 || id == "." || id == ".." || !idPattern.MatchString(id) {
		return fmt.Errorf("check ID %q is invalid", id)
	}
	if check.Command == "" || len(check.Command) > 512 || len(check.Args) > 128 {
		return fmt.Errorf("check %s has an invalid command", id)
	}
	if check.TimeoutSeconds < 0 || check.TimeoutSeconds > 1800 {
		return fmt.Errorf("check %s timeout must be between 0 and 1800 seconds", id)
	}
	if check.Image != "" && !checkImagePattern.MatchString(check.Image) {
		return fmt.Errorf("check %s: image must be pinned by digest (name@sha256:<64 lowercase hex>)", id)
	}
	if err := validateCheckDependencySettings(check); err != nil {
		return fmt.Errorf("check %s: %w", id, err)
	}
	return nil
}
