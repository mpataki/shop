package models

// SignalStatus represents the completion status an agent reports via report_signal.
type SignalStatus string

const (
	SignalDone             SignalStatus = "DONE"
	SignalBlocked          SignalStatus = "BLOCKED"
	SignalNeedsHuman       SignalStatus = "NEEDS_HUMAN"
	SignalApproved         SignalStatus = "APPROVED"
	SignalChangesRequested SignalStatus = "CHANGES_REQUESTED"
	SignalContinue         SignalStatus = "CONTINUE"
	SignalStop             SignalStatus = "STOP"
	SignalError            SignalStatus = "ERROR" // internal only, not agent-facing
)

// ValidAgentStatuses are the statuses agents can report via the MCP tool.
var ValidAgentStatuses = []SignalStatus{
	SignalDone,
	SignalBlocked,
	SignalNeedsHuman,
	SignalApproved,
	SignalChangesRequested,
	SignalContinue,
	SignalStop,
}

// IsValid returns true if the status is in the set of valid agent-facing statuses.
func (s SignalStatus) IsValid() bool {
	for _, v := range ValidAgentStatuses {
		if s == v {
			return true
		}
	}
	return false
}

// ValidAgentStatusStrings returns the valid statuses as strings (for JSON schema enums).
func ValidAgentStatusStrings() []string {
	out := make([]string, len(ValidAgentStatuses))
	for i, s := range ValidAgentStatuses {
		out[i] = string(s)
	}
	return out
}
