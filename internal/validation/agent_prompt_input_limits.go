package validation

const (
	MaxTicketSummaryBytes      = 1 << 10
	MaxTicketDescriptionBytes  = 256 << 10
	MaxAcceptanceCriteriaBytes = 128 << 10
	MaxReviewCommentBytes      = 128 << 10
	MaxRequiredChecks          = 64
	MaxRequiredCheckBytes      = maxIDLength
)
