package streamingtoolexecutor

import "context"

type Hook interface {
	BeforeExecute(ctx context.Context, call *ToolCall) error
	AfterExecute(ctx context.Context, call *ToolCall, result *ToolResult) error
}
