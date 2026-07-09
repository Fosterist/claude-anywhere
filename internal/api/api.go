// Package api defines the HTTP contract between the bot (job queue owner)
// and the agent (job executor). Bot and agent may run on the same machine
// or, later, on different ones — everything goes through this HTTP surface
// so moving the bot to a VPS is a config change, not a redesign.
package api

// Job is a unit of work: run one prompt against one project.
type Job struct {
	ID          int64  `json:"id"`
	Project     string `json:"project"`
	Prompt      string `json:"prompt"`
	SessionID   string `json:"session_id,omitempty"`   // set when resuming a prior run
	Permission  string `json:"permission"`              // claude --permission-mode value
	MaxBudget   float64 `json:"max_budget_usd,omitempty"`
}

// Result is what the agent reports back after running a job.
type Result struct {
	JobID      int64   `json:"job_id"`
	Result     string  `json:"result"`
	SessionID  string  `json:"session_id"`
	CostUSD    float64 `json:"cost_usd"`
	IsError    bool    `json:"is_error"`
	ErrorText  string  `json:"error_text,omitempty"`
}
