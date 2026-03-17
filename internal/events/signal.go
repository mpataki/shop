package events

// SignalStatus represents the completion status an agent reports via report_signal.
type SignalStatus string

// Reserved statuses have special runtime behavior and are always available.
const (
	SignalDone       SignalStatus = "DONE"
	SignalStuck      SignalStatus = "STUCK"
	SignalNeedsHuman SignalStatus = "NEEDS_HUMAN"
	SignalError      SignalStatus = "ERROR" // internal only, never in agent-facing enum
)

// ReservedStatuses are always included in the agent-facing status enum.
var ReservedStatuses = []string{"DONE", "STUCK", "NEEDS_HUMAN"}

// MergeStatuses returns reserved statuses + custom, deduplicating and excluding ERROR.
func MergeStatuses(custom []string) []string {
	seen := make(map[string]bool, len(ReservedStatuses)+len(custom))
	out := make([]string, 0, len(ReservedStatuses)+len(custom))
	for _, s := range ReservedStatuses {
		seen[s] = true
		out = append(out, s)
	}
	for _, s := range custom {
		if s == string(SignalError) || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
