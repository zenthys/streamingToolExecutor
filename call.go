package streamingtoolexecutor

import "encoding/json"

type State uint8

const (
	StateReceiving State = iota
	StateValidating
	StateScheduled
	StateRunning
	StateSucceeded
	StateFailed
)

type ToolCallDelta struct {
	ID             string
	NameDelta      string
	ArgumentsDelta string
	Done           bool
}

type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
	Metadata  map[string]any
}

type callState struct {
	id        string
	name      string
	arguments []byte
	state     State
	done      bool
	result    ToolResult
}
