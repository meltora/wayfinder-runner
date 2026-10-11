package capability

func (d *Detector) WithAgentPackTrust(rootKeyIDs, trustedKeyIDs []string) *Detector {
	configured := *d
	configured.agentPackTrustChain = true
	configured.agentPackTrustRootKeyIDs = nil
	if roots := uniqueSorted(rootKeyIDs); len(roots) != 0 {
		configured.agentPackTrustRootKeyIDs = roots
	}
	configured.trustedAgentPackKeyIDs = uniqueSorted(trustedKeyIDs)
	return &configured
}
