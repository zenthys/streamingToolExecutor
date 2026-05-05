package streamingtoolexecutor

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type testTool struct {
	name string
	fn   func(context.Context, json.RawMessage) (any, error)
}

func (t testTool) Name() string { return t.name }

func (t testTool) Execute(ctx context.Context, args json.RawMessage) (any, error) {
	return t.fn(ctx, args)
}

func testObjectSchema(t *testing.T) any {
	t.Helper()
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"city": map[string]any{"type": "string"},
		},
		"required":             []any{"city"},
		"additionalProperties": false,
	}
}

func registerTestTool(t *testing.T, e *Executor, tool Tool) {
	t.Helper()
	schema, err := CompileSchema(testObjectSchema(t))
	if err != nil {
		t.Fatalf("CompileSchema: %v", err)
	}
	if err := e.Register(tool, schema); err != nil {
		t.Fatalf("Register: %v", err)
	}
}

func TestPushStartsToolBeforeCollect(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	e := New(context.Background())
	registerTestTool(t, e, testTool{name: "weather", fn: func(ctx context.Context, args json.RawMessage) (any, error) {
		close(started)
		<-release
		return "sunny", nil
	}})

	if err := e.Push(ToolCallDelta{ID: "call-1", NameDelta: "weather", ArgumentsDelta: `{"city":"Shanghai"}`}); err != nil {
		t.Fatalf("Push: %v", err)
	}

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("tool did not start before Collect")
	}

	close(release)
	results, err := e.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(results) != 1 || results[0].Err != nil || results[0].Value != "sunny" {
		t.Fatalf("unexpected results: %#v", results)
	}
}

func TestToolExecutesExactlyOnce(t *testing.T) {
	var executions atomic.Int32
	e := New(context.Background())
	registerTestTool(t, e, testTool{name: "weather", fn: func(ctx context.Context, args json.RawMessage) (any, error) {
		executions.Add(1)
		return nil, nil
	}})

	if err := e.Push(ToolCallDelta{ID: "call-1", NameDelta: "weather", ArgumentsDelta: `{"city":"Shanghai"}`}); err != nil {
		t.Fatalf("first Push: %v", err)
	}
	if err := e.Push(ToolCallDelta{ID: "call-1", ArgumentsDelta: "ignored", Done: true}); err != nil {
		t.Fatalf("second Push: %v", err)
	}
	if _, err := e.Collect(context.Background()); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got := executions.Load(); got != 1 {
		t.Fatalf("executions = %d, want 1", got)
	}
}

func TestInvalidSchemaWaitsUntilDone(t *testing.T) {
	var executions atomic.Int32
	e := New(context.Background())
	registerTestTool(t, e, testTool{name: "weather", fn: func(ctx context.Context, args json.RawMessage) (any, error) {
		executions.Add(1)
		return nil, nil
	}})

	if err := e.Push(ToolCallDelta{ID: "call-1", NameDelta: "weather", ArgumentsDelta: `{}`}); err != nil {
		t.Fatalf("Push incomplete semantic args: %v", err)
	}
	if got := executions.Load(); got != 0 {
		t.Fatalf("executions = %d before Done, want 0", got)
	}
	if err := e.Push(ToolCallDelta{ID: "call-1", Done: true}); err != nil {
		t.Fatalf("Push Done: %v", err)
	}

	results, err := e.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(results) != 1 || !errors.Is(results[0].Err, ErrInvalidArguments) {
		t.Fatalf("unexpected result: %#v", results)
	}
	if got := executions.Load(); got != 0 {
		t.Fatalf("executions = %d, want 0", got)
	}
}

func TestCollectFinalizesIncompleteJSON(t *testing.T) {
	e := New(context.Background())
	registerTestTool(t, e, testTool{name: "weather", fn: func(context.Context, json.RawMessage) (any, error) {
		t.Fatal("tool must not execute")
		return nil, nil
	}})

	if err := e.Push(ToolCallDelta{ID: "call-1", NameDelta: "weather", ArgumentsDelta: `{"city":`}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	results, err := e.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(results) != 1 || !errors.Is(results[0].Err, ErrIncompleteArguments) {
		t.Fatalf("unexpected result: %#v", results)
	}
}

type recordingHook struct {
	name   string
	mu     *sync.Mutex
	events *[]string
}

func (h recordingHook) BeforeExecute(context.Context, *ToolCall) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	*h.events = append(*h.events, h.name+".before")
	return nil
}

func (h recordingHook) AfterExecute(context.Context, *ToolCall, *ToolResult) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	*h.events = append(*h.events, h.name+".after")
	return nil
}

func TestHooksAreStackOrdered(t *testing.T) {
	var mu sync.Mutex
	var events []string
	e := New(
		context.Background(),
		WithHook(recordingHook{name: "A", mu: &mu, events: &events}),
		WithHook(recordingHook{name: "B", mu: &mu, events: &events}),
	)
	registerTestTool(t, e, testTool{name: "weather", fn: func(context.Context, json.RawMessage) (any, error) {
		mu.Lock()
		events = append(events, "tool")
		mu.Unlock()
		return nil, nil
	}})

	if err := e.Push(ToolCallDelta{ID: "call-1", NameDelta: "weather", ArgumentsDelta: `{"city":"Shanghai"}`}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if _, err := e.Collect(context.Background()); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	want := []string{"A.before", "B.before", "tool", "B.after", "A.after"}
	if len(events) != len(want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("events = %v, want %v", events, want)
		}
	}
}
