package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	streamtool "github.com/zenthys/streamingToolExecutor"
)

type WeatherTool struct{}

func (WeatherTool) Name() string {
	return "get_weather"
}

func (WeatherTool) Execute(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		City string `json:"city"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, err
	}

	// 模拟真实工具的网络 / I/O 延迟。
	select {
	case <-time.After(300 * time.Millisecond):
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	return map[string]any{
		"city":        args.City,
		"temperature": 26,
		"condition":   "sunny",
	}, nil
}

type LoggingHook struct{}

func (LoggingHook) BeforeExecute(ctx context.Context, call *streamtool.ToolCall) error {
	log.Printf("tool start: id=%s name=%s args=%s", call.ID, call.Name, call.Arguments)
	return nil
}

func (LoggingHook) AfterExecute(
	ctx context.Context,
	call *streamtool.ToolCall,
	result *streamtool.ToolResult,
) error {
	log.Printf("tool finish: id=%s name=%s err=%v", call.ID, call.Name, result.Err)
	return nil
}

func main() {
	ctx := context.Background()

	executor := streamtool.New(
		ctx,
		streamtool.WithMaxConcurrency(4),
		streamtool.WithHook(LoggingHook{}),
	)

	schema, err := streamtool.CompileSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"city": map[string]any{
				"type": "string",
			},
		},
		"required":             []any{"city"},
		"additionalProperties": false,
	})
	if err != nil {
		log.Fatal(err)
	}

	if err := executor.Register(WeatherTool{}, schema); err != nil {
		log.Fatal(err)
	}

	// 模拟 LLM provider 的流式 Tool Call。
	deltas := []streamtool.ToolCallDelta{
		{
			ID:             "call_weather_1",
			NameDelta:      "get_weather",
			ArgumentsDelta: `{"ci`,
		},
		{
			ID:             "call_weather_1",
			ArgumentsDelta: `ty":"Shang`,
		},
		{
			ID:             "call_weather_1",
			ArgumentsDelta: `hai"}`,
		},
	}

	for i, delta := range deltas {
		if err := executor.Push(delta); err != nil {
			log.Fatal(err)
		}

		fmt.Printf("model stream: received delta %d\\n", i+1)

		// 模拟模型第三个 delta 之后仍然继续吐 token。
		time.Sleep(100 * time.Millisecond)
	}

	fmt.Println("model stream: still generating tokens")
	time.Sleep(150 * time.Millisecond)
	fmt.Println("model stream: EOF")

	results, err := executor.Collect(ctx)
	if err != nil {
		log.Fatal(err)
	}

	for _, result := range results {
		if result.Err != nil {
			fmt.Printf("%s failed: %v\\n", result.CallID, result.Err)
			continue
		}

		fmt.Printf(
			"%s succeeded in %s: %#v\\n",
			result.CallID,
			result.FinishedAt.Sub(result.StartedAt),
			result.Value,
		)
	}
}
