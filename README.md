# Streaming Tool Executor

用于 LLM Tool Calling 的 Go 流式工具执行器。

模型流式输出 Tool Call 参数时，通过 `Push` 持续传入参数。参数完整并通过 JSON Schema 校验后，工具会立即开始执行。模型流结束后，通过 `Collect` 一次性等待并获取所有工具结果。

## 安装

```bash
go get github.com/zenthys/streamingToolExecutor
```

## 基本用法

使用步骤：

1. 实现 `Tool` 接口
2. 为 Tool 定义 JSON Schema
3. 创建 Executor 并注册 Tool
4. 模型流式返回 Tool Call 时调用 `Push`
5. 模型流结束后调用 `Collect`

### 1. 实现 Tool

用户必须实现 `Tool` 接口：

```go
type Tool interface {
    Name() string
    Execute(ctx context.Context, args json.RawMessage) (any, error)
}
```

例如：

```go
type WeatherTool struct{}

func (WeatherTool) Name() string {
    return "get_weather"
}

func (WeatherTool) Execute(
    ctx context.Context,
    args json.RawMessage,
) (any, error) {
    var input struct {
        City string `json:"city"`
    }

    if err := json.Unmarshal(args, &input); err != nil {
        return nil, err
    }

    return "weather of " + input.City, nil
}
```

`Name()` 返回模型调用时使用的工具名称。

`Execute()` 是实际的工具执行逻辑，`args` 是已经通过 JSON Schema 校验的完整参数。

### 2. 定义参数 Schema

```go
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
    return err
}
```

参数校验使用：

```text
github.com/santhosh-tekuri/jsonschema/v6
```

### 3. 创建 Executor 并注册 Tool

```go
executor := streamtool.New(
    ctx,
    streamtool.WithMaxConcurrency(8),
)

if err := executor.Register(WeatherTool{}, schema); err != nil {
    return err
}
```

`WithMaxConcurrency` 用于限制同时执行的工具数量。

### 4. 流式 Push Tool Call

将模型返回的 Tool Call 增量转换成：

```go
streamtool.ToolCallDelta{
    ID:             call.ID,
    NameDelta:      call.NameDelta,
    ArgumentsDelta: call.ArgumentsDelta,
    Done:           call.Done,
}
```

然后持续 Push：

```go
for event := range modelStream {
    for _, call := range event.ToolCalls {
        err := executor.Push(streamtool.ToolCallDelta{
            ID:             call.ID,
            NameDelta:      call.NameDelta,
            ArgumentsDelta: call.ArgumentsDelta,
            Done:           call.Done,
        })
        if err != nil {
            return err
        }
    }
}
```

当某个 Tool Call 的参数已经是完整 JSON，并且通过 Schema 校验后，对应 Tool 会立即在后台开始执行，不需要等待模型流结束。

### 5. 收集结果

模型流结束后调用：

```go
results, err := executor.Collect(ctx)
if err != nil {
    return err
}

for _, result := range results {
    if result.Err != nil {
        log.Printf("tool %s failed: %v", result.Name, result.Err)
        continue
    }

    log.Printf("tool %s result: %#v", result.Name, result.Value)
}
```

`Collect` 会阻塞等待尚未完成的工具，然后一次性返回结果。

单个 Tool 执行失败时，错误位于：

```go
result.Err
```

不会导致其他 Tool 的结果丢失。

## Hook

Hook 是可选的。

如果需要在工具执行前后加入日志、Tracing、Metrics 等逻辑，实现：

```go
type Hook interface {
    BeforeExecute(
        ctx context.Context,
        call *ToolCall,
    ) error

    AfterExecute(
        ctx context.Context,
        call *ToolCall,
        result *ToolResult,
    ) error
}
```

例如：

```go
type LoggingHook struct{}

func (LoggingHook) BeforeExecute(
    ctx context.Context,
    call *streamtool.ToolCall,
) error {
    log.Printf("start tool: %s", call.Name)
    return nil
}

func (LoggingHook) AfterExecute(
    ctx context.Context,
    call *streamtool.ToolCall,
    result *streamtool.ToolResult,
) error {
    log.Printf("finish tool: %s", call.Name)
    return nil
}
```

注册：

```go
executor := streamtool.New(
    ctx,
    streamtool.WithHook(LoggingHook{}),
)
```

不需要 Hook 时无需实现该接口。

## 完整示例

```go
ctx := context.Background()

executor := streamtool.New(
    ctx,
    streamtool.WithMaxConcurrency(8),
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
    return err
}

if err := executor.Register(WeatherTool{}, schema); err != nil {
    return err
}

for event := range modelStream {
    for _, call := range event.ToolCalls {
        if err := executor.Push(streamtool.ToolCallDelta{
            ID:             call.ID,
            NameDelta:      call.NameDelta,
            ArgumentsDelta: call.ArgumentsDelta,
            Done:           call.Done,
        }); err != nil {
            return err
        }
    }
}

results, err := executor.Collect(ctx)
if err != nil {
    return err
}

for _, result := range results {
    if result.Err != nil {
        log.Printf("%s failed: %v", result.Name, result.Err)
        continue
    }

    log.Printf("%s result: %#v", result.Name, result.Value)
}
```

更完整的可运行示例见：

```text
examples/basic/main.go
```