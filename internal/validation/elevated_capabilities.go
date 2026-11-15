package validation

import (
	"fmt"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func ElevatedCapabilities(value v1.ElevatedCapabilityGrants) error {
	if value.SchemaVersion != v1.ElevatedCapabilityTransportVersion {
		return fmt.Errorf("elevated_capabilities schema_version is unsupported")
	}
	if len(value.Grants) < 1 || len(value.Grants) > v1.MaxElevatedCapabilityGrants {
		return fmt.Errorf("elevated_capabilities grants are out of range")
	}
	seen := map[string]bool{}
	for index, grant := range value.Grants {
		field := fmt.Sprintf("elevated_capabilities.grants[%d]", index)
		if err := ID(field+".request_id", grant.RequestID); err != nil {
			return err
		}
		if seen[grant.RequestID] {
			return fmt.Errorf("%s.request_id appears more than once", field)
		}
		seen[grant.RequestID] = true
	}
	return nil
}
