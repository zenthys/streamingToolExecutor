package streamingtoolexecutor

import "errors"

var (
	ErrClosed              = errors.New("streaming tool executor is closed")
	ErrIncompleteArguments = errors.New("incomplete tool arguments")
	ErrUnknownTool         = errors.New("unknown tool")
	ErrInvalidArguments    = errors.New("invalid tool arguments")
)
