package streamingtoolexecutor

import "time"

type ToolResult struct {
	CallID     string
	Name       string
	Value      any
	Err        error
	StartedAt  time.Time
	FinishedAt time.Time
}
