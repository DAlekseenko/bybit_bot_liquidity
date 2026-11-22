package paperwire

import "encoding/json"

type scopeWire struct {
	Environment string `json:"environment"`
	AccountID   string `json:"account_id"`
	StrategyID  string `json:"strategy_id"`
}
type purposeWire struct {
	Kind            string `json:"kind"`
	CommandID       string `json:"command_id"`
	ControlRevision string `json:"control_revision"`
}
type runWire struct {
	RunID         string  `json:"run_id"`
	ScheduledAt   instant `json:"scheduled_at"`
	OnboardingID  string  `json:"onboarding_id"`
	ConfigPayload string  `json:"config_payload"`
	ConfigHash    string  `json:"config_sha256"`
}
type inputWire struct {
	Format, Sequence, PreviousInputHash, PreviousResolutionHash string
	Scope                                                       scopeWire
	Purpose                                                     purposeWire
	Run                                                         runWire
	Model                                                       json.RawMessage
	ModelHash                                                   string
	Observation                                                 json.RawMessage
	Before, Cutoff, ExpectedAfter                               stateWire
	PlanPayload, PlanHash                                       string
	Fills                                                       []fillWire
}

// inputFields задаёт нормативный порядок корневых полей входа.
func inputFields(w *inputWire) []field {
	return []field{{"format", &w.Format}, {"scope", &w.Scope}, {"sequence", &w.Sequence},
		{"previous_input_sha256", &w.PreviousInputHash}, {"previous_resolution_sha256", &w.PreviousResolutionHash},
		{"purpose", &w.Purpose}, {"run", &w.Run}, {"model", &w.Model}, {"model_sha256", &w.ModelHash},
		{"observation", &w.Observation}, {"before", &w.Before}, {"cutoff", &w.Cutoff},
		{"plan_payload", &w.PlanPayload}, {"plan_sha256", &w.PlanHash}, {"fills", &w.Fills}, {"expected_after", &w.ExpectedAfter}}
}
