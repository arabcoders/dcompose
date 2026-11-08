package protocol

// StackAction describes a live command lifecycle, not a retained operation.
type StackAction struct {
	Name     string `json:"name"`
	Action   string `json:"action"`
	Phase    string `json:"phase"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Message  string `json:"message,omitempty"`
}

// StackOutput is delivered only to currently connected runtime subscribers.
type StackOutput struct {
	Name   string `json:"name"`
	Output string `json:"output"`
}
