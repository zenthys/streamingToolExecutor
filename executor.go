package streamingtoolexecutor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

type Executor struct {
	ctx    context.Context
	cancel context.CancelFunc

	registry *Registry
	hooks    []Hook

	mu             sync.Mutex
	calls          map[string]*callState
	order          []string
	closed         bool
	wg             sync.WaitGroup
	sem            chan struct{}
	maxConcurrency int
}

func New(parent context.Context, opts ...Option) *Executor {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	e := &Executor{
		ctx:            ctx,
		cancel:         cancel,
		registry:       newRegistry(),
		calls:          make(map[string]*callState),
		maxConcurrency: 8,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(e)
		}
	}
	e.sem = make(chan struct{}, e.maxConcurrency)
	return e
}

func (e *Executor) Register(tool Tool, schema *jsonschema.Schema) error {
	return e.registry.register(tool, schema)
}

func (e *Executor) Push(delta ToolCallDelta) error {
	if delta.ID == "" {
		return fmt.Errorf("push: empty call id")
	}

	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return ErrClosed
	}

	call, ok := e.calls[delta.ID]
	if !ok {
		call = &callState{id: delta.ID, state: StateReceiving}
		e.calls[delta.ID] = call
		e.order = append(e.order, delta.ID)
	}

	if call.state != StateReceiving {
		e.mu.Unlock()
		return nil
	}

	call.name += delta.NameDelta
	call.arguments = append(call.arguments, delta.ArgumentsDelta...)
	call.done = call.done || delta.Done

	raw := append([]byte(nil), call.arguments...)
	name := call.name
	done := call.done

	if json.Valid(raw) {
		if entry, exists := e.registry.get(name); exists {
			if err := validate(entry.schema, raw); err == nil {
				call.state = StateScheduled
				e.wg.Add(1)
				e.mu.Unlock()
				go e.execute(call.id, name, raw, entry)
				return nil
			} else if done {
				e.failLocked(call, fmt.Errorf("%w: %v", ErrInvalidArguments, err))
			}
		} else if done {
			e.failLocked(call, fmt.Errorf("%w: %s", ErrUnknownTool, name))
		}
	} else if done {
		e.failLocked(call, ErrIncompleteArguments)
	}

	e.mu.Unlock()
	return nil
}

func validate(schema *jsonschema.Schema, raw []byte) error {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	return schema.Validate(value)
}

func (e *Executor) execute(id, name string, raw []byte, entry *toolEntry) {
	defer e.wg.Done()

	select {
	case e.sem <- struct{}{}:
		defer func() { <-e.sem }()
	case <-e.ctx.Done():
		e.finish(id, nil, e.ctx.Err())
		return
	}

	call := &ToolCall{
		ID:        id,
		Name:      name,
		Arguments: append([]byte(nil), raw...),
		Metadata:  make(map[string]any),
	}
	result := ToolResult{CallID: id, Name: name}
	result.StartedAt = time.Now()

	e.setState(id, StateRunning)

	var err error
	for _, hook := range e.hooks {
		if err = hook.BeforeExecute(e.ctx, call); err != nil {
			break
		}
	}

	if err == nil {
		result.Value, err = entry.tool.Execute(e.ctx, call.Arguments)
	}
	result.Err = err

	for i := len(e.hooks) - 1; i >= 0; i-- {
		if hookErr := e.hooks[i].AfterExecute(e.ctx, call, &result); hookErr != nil {
			result.Err = errors.Join(result.Err, hookErr)
		}
	}

	result.FinishedAt = time.Now()
	e.storeResult(id, result)
}

func (e *Executor) Collect(ctx context.Context) ([]ToolResult, error) {
	e.finalize()

	done := make(chan struct{})
	go func() {
		e.wg.Wait()
		close(done)
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-done:
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	results := make([]ToolResult, 0, len(e.order))
	for _, id := range e.order {
		results = append(results, e.calls[id].result)
	}
	e.cancel()
	return results, nil
}

func (e *Executor) finalize() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return
	}
	e.closed = true

	for _, call := range e.calls {
		if call.state != StateReceiving {
			continue
		}
		entry, ok := e.registry.get(call.name)
		if !ok {
			e.failLocked(call, fmt.Errorf("%w: %s", ErrUnknownTool, call.name))
			continue
		}
		if !json.Valid(call.arguments) {
			e.failLocked(call, ErrIncompleteArguments)
			continue
		}
		if err := validate(entry.schema, call.arguments); err != nil {
			e.failLocked(call, fmt.Errorf("%w: %v", ErrInvalidArguments, err))
			continue
		}
		call.state = StateScheduled
		raw := append([]byte(nil), call.arguments...)
		e.wg.Add(1)
		go e.execute(call.id, call.name, raw, entry)
	}
}

func (e *Executor) failLocked(call *callState, err error) {
	call.state = StateFailed
	call.result = ToolResult{CallID: call.id, Name: call.name, Err: err}
}

func (e *Executor) setState(id string, state State) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if call := e.calls[id]; call != nil {
		call.state = state
	}
}

func (e *Executor) finish(id string, value any, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	call := e.calls[id]
	if call == nil {
		return
	}
	call.result = ToolResult{CallID: id, Name: call.name, Value: value, Err: err}
	if err != nil {
		call.state = StateFailed
	} else {
		call.state = StateSucceeded
	}
}

func (e *Executor) storeResult(id string, result ToolResult) {
	e.mu.Lock()
	defer e.mu.Unlock()
	call := e.calls[id]
	if call == nil {
		return
	}
	call.result = result
	if result.Err != nil {
		call.state = StateFailed
	} else {
		call.state = StateSucceeded
	}
}
