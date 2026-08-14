package v1

type PlanContent struct {
	Behavior              string   `json:"behavior"`
	InScope               []string `json:"in_scope"`
	OutOfScope            []string `json:"out_of_scope"`
	Assumptions           []string `json:"assumptions"`
	UnansweredQuestions   []string `json:"unanswered_questions"`
	Architecture          string   `json:"architecture"`
	APIImpact             string   `json:"api_impact"`
	DataImpact            string   `json:"data_impact"`
	SecurityImpact        string   `json:"security_impact"`
	OperationalImpact     string   `json:"operational_impact"`
	PreviewRequirements   string   `json:"preview_requirements"`
	AutomatedTestStrategy string   `json:"automated_test_strategy"`
	ManualTestPlan        string   `json:"manual_test_plan"`
	RolloutStrategy       string   `json:"rollout_strategy"`
	RollbackStrategy      string   `json:"rollback_strategy"`
	Risks                 []string `json:"risks"`
	Alternatives          []string `json:"alternatives"`
	Dependencies          []string `json:"dependencies"`
	DeliveryOrder         []string `json:"delivery_order"`
}
