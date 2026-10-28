package daemon

const planCheckoutVerificationFailure = "plan repository checkout verification failed"

func planCheckoutMismatchCause(repository checkedOutRepository, baseSHA string) string {
	head, err := planCheckoutHead(repository.WorkDir)
	if err != nil {
		return "head_unreadable"
	}
	if head != baseSHA {
		return "head_mismatch"
	}
	expected := repository.PlanBaseline.current().Strict
	if expected == "" {
		return "baseline_missing"
	}
	observed, err := planCheckoutBaseline(repository.WorkDir)
	if err != nil {
		return "baseline_unreadable"
	}
	if observed.Strict != expected {
		return "baseline_mismatch"
	}
	return ""
}
