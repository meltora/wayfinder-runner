package v1

type ManifestAuthority struct {
	Kind             JobKind `json:"manifest_kind"`
	LeaseID          string  `json:"lease_id"`
	ManifestID       string  `json:"manifest_id"`
	ManifestDigest   string  `json:"manifest_digest"`
	WorkflowRevision int     `json:"workflow_revision"`
}

func (j JobSpec) ManifestAuthority() ManifestAuthority {
	return ManifestAuthority{
		Kind:             j.Kind,
		LeaseID:          j.LeaseID,
		ManifestID:       j.ManifestID,
		ManifestDigest:   j.ManifestDigest,
		WorkflowRevision: j.WorkflowRevision,
	}
}
