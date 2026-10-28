package daemon

import "sync"

type planCheckoutEvidence struct {
	mu      sync.Mutex
	digests planCheckoutDigests
}

func newPlanCheckoutEvidence(digests planCheckoutDigests) *planCheckoutEvidence {
	return &planCheckoutEvidence{digests: digests}
}

func (evidence *planCheckoutEvidence) current() planCheckoutDigests {
	if evidence == nil {
		return planCheckoutDigests{}
	}
	evidence.mu.Lock()
	defer evidence.mu.Unlock()
	return evidence.digests
}

func (evidence *planCheckoutEvidence) carryForward(observed planCheckoutDigests) bool {
	if evidence == nil || observed.Identity == "" || observed.Strict == "" {
		return false
	}
	evidence.mu.Lock()
	defer evidence.mu.Unlock()
	if evidence.digests.Identity == "" || evidence.digests.Identity != observed.Identity {
		return false
	}
	evidence.digests = observed
	return true
}
