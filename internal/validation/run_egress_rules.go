package validation

import (
	"fmt"
	"regexp"

	v1 "github.com/example/app-runner/api/runner/v1"
)

var (
	egressEnforcementIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	egressApprovedHostPattern  = regexp.MustCompile(`^(\*\.)?([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$`)
)

func EgressApprovedHosts(hosts []string) error {
	if hosts == nil || len(hosts) > v1.MaxEgressApprovedHosts {
		return fmt.Errorf("egress approved_hosts must be a list of at most %d hosts", v1.MaxEgressApprovedHosts)
	}
	for index, host := range hosts {
		if len(host) > 255 || !egressApprovedHostPattern.MatchString(host) {
			return fmt.Errorf("egress approved_hosts contains an invalid host")
		}
		if index > 0 && hosts[index-1] >= host {
			return fmt.Errorf("egress approved_hosts must be sorted and unique")
		}
	}
	return nil
}

func RunEgressRules(value v1.RunEgressRules) error {
	if !egressEnforcementIDPattern.MatchString(value.EnforcementID) {
		return fmt.Errorf("egress_rules enforcement_id is invalid")
	}
	if err := SHA256Digest("egress_rules rules_digest", value.RulesDigest); err != nil {
		return err
	}
	if err := EgressApprovedHosts(value.ApprovedHosts); err != nil {
		return err
	}
	if value.ApprovedHostsDigest != v1.EgressApprovedHostsDigest(value.ApprovedHosts) {
		return fmt.Errorf("egress_rules approved_hosts_digest does not bind the approved hosts")
	}
	return nil
}
