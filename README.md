# Streaming Tool Executor

一个面向 LLM Tool Calling 场景的 Go 流式工具执行器。

核心目标只有两个动作：

```go
executor.Push(delta)          // 边接收 tool call 参数，满足条件立即执行
results, err := executor.Collect(ctx) // 模型流结束后，一次性收集全部结果
```

传统 Tool Calling 通常是完全串行的：

```text
LLM streaming  ███████████████████
                                    Tool ███████████
```

Streaming Tool Executor 会在模型仍然输出后续 token 时启动已经准备好的工具：

```text
LLM streaming  █████████████████████████
                       │
                       └── Tool A ███████████
                              └── Tool B ███████
```

这样可以重叠模型生成时间与网络、数据库或其他 I/O 型工具的执行时间，减少完整 Tool Calling Round Trip 的等待。

## 特性

- 流式接收 Tool Call delta
- 参数准备完成后立即启动工具，而不是等待模型流结束
- 使用 `github.com/santhosh-tekuri/jsonschema/v6` 校验工具参数
- Schema 预编译并在执行阶段复用
- 同一 `call_id` exactly-once 执行
- 多工具并发执行
- `MaxConcurrency` 并发上限
- Before / After Hook 插件机制
- After Hook 按栈顺序逆序执行
- `Collect` 作为最终 barrier
- 单个工具错误通过 `ToolResult.Err` 返回，不污染其他工具结果
- 保持 Tool Call 首次出现顺序返回结果

## 安装

```bash
go get github.com/zenthys/streamingToolExecutor
```

项目使用：

```text
github.com/santhosh-tekuri/jsonschema/v6
```

进行 JSON Schema 参数校验。

## 快速开始

下面实现一个天气查询工具。

```go
package main

import (
    \"context\"
    \"encoding/json\"
    \"fmt\"
    \"log\"

    streamtool \"github.com/zenthys/streamingToolExecutor\"
)

type WeatherTool struct{}

func (WeatherTool) Name() string {
    return \"get_weather\"
}

func (WeatherTool) Execute(
    ctx context.Context,
    raw json.RawMessage,
) (any, error) {
    var args struct {
        City string `json:\"city\"`
    }

    if err := json.Unmarshal(raw, &args); err != nil {
        return nil, err
    }

    return map[string]any{
        \"city\":        args.City,
        \"temperature\": 26,
        \"condition\":   \"sunny\",
    }, nil
}

func main() {
    ctx := context.Background()

    executor := streamtool.New(
        ctx,
        streamtool.WithMaxConcurrency(8),
    )

    schema, err := streamtool.CompileSchema(map[string]any{
        \"type\": \"object\",
        \"properties\": map[string]any{
            \"city\": map[string]any{
                \"type\": \"string\",
            },
        },
        \"required\":             []any{\"city\"},
        \"additionalProperties\": false,
    })
    if err != nil {
        log.Fatal(err)
    }

    if err := executor.Register(WeatherTool{}, schema); err != nil {
        log.Fatal(err)
    }

    // 模拟模型分多次返回 tool call arguments。
    deltas := []streamtool.ToolCallDelta{
        {
            ID:        \"call_1\",
            NameDelta: \"get_weather\",
            ArgumentsDelta: `{\"ci`,
        },
        {
            ID:             \"call_1\",
            ArgumentsDelta: `ty\":\"Shang`,
        },
        {
            ID:             \"call_1\",
            ArgumentsDelta: `hai\"}`,
        },
    }

    for _, delta := range deltas {
        if err := executor.Push(delta); err != nil {
            log.Fatal(err)
        }
    }

    // 当第三个 delta 使参数成为完整且满足 Schema 的 JSON 时，
    // get_weather 已经可以在这里后台执行。
    //
    // 此时模型仍然可以继续流式输出其他内容或其他 tool calls。

    results, err := executor.Collect(ctx)
    if err != nil {
        log.Fatal(err)
    }

    for _, result := range results {
        if result.Err != nil {
            fmt.Printf(\"tool %s failed: %v\\n\", result.Name, result.Err)
            continue
        }

        fmt.Printf(\"tool %s result: %#v\\n\", result.Name, result.Value)
    }
}
```

## 核心执行语义

一个 Tool Call 在满足以下三个条件后立即启动：

```text
JSON syntactically complete
        AND
JSON Schema valid
        AND
Tool exists
        AND
Call has not already started
```

因此执行器不会等待整个 LLM stream 完成。

例如模型依次返回：

```text
delta 1: {\"ci
delta 2: ty\":\"Shang
delta 3: hai\"}
```

内部参数状态依次为：

```json
{\"ci
{\"city\":\"Shang
{\"city\":\"Shanghai\"}
```

前两个状态不是完整 JSON，因此继续等待。

第三个状态已经：

1. 是完整 JSON
2. 满足工具 JSON Schema
3. 找得到对应工具

执行器立即调度工具。

## 为什么不能只使用 json.Valid

`json.Valid` 只能判断 JSON 语法是否完整。

例如：

```json
{}
```

这是有效 JSON，但如果 Schema 是：

```json
{
  \"type\": \"object\",
  \"properties\": {
    \"city\": {
      \"type\": \"string\"
    }
  },
  \"required\": [\"city\"]
}
```

那么 `{}` 并不是合法工具参数。

因此执行器分成两步判断：

```text
json.Valid
    ↓
jsonschema.Validate
    ↓
schedule
```

并且流式阶段第一次 Schema 校验失败不会立即认定整个 Tool Call 失败。

只有收到 `Done: true`，或者最终执行 `Collect()` 时，仍然无法形成合法参数，才会生成最终参数错误。

## Tool

工具只需要实现：

```go
type Tool interface {
    Name() string

    Execute(
        ctx context.Context,
        args json.RawMessage,
    ) (any, error)
}
```

例如：

```go
type SearchTool struct{}

func (SearchTool) Name() string {
    return \"search\"
}

func (SearchTool) Execute(
    ctx context.Context,
    args json.RawMessage,
) (any, error) {
    // 执行业务逻辑
    return result, nil
}
```

## JSON Schema

推荐在注册 Tool 时编译 Schema：

```go
schema, err := streamtool.CompileSchema(map[string]any{
    \"type\": \"object\",
    \"properties\": map[string]any{
        \"query\": map[string]any{
            \"type\": \"string\",
        },
        \"limit\": map[string]any{
            \"type\":    \"integer\",
            \"minimum\": 1,
            \"maximum\": 20,
        },
    },
    \"required\": []any{\"query\"},
    \"additionalProperties\": false,
})
```

然后注册：

```go
err = executor.Register(SearchTool{}, schema)
```

Schema 编译发生在注册准备阶段，执行工具时只复用编译后的 `*jsonschema.Schema`。

## Hook 插件

Hook 可以包装 Tool Execute 生命周期：

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

例如日志 Hook：

```go
type LoggingHook struct{}

func (LoggingHook) BeforeExecute(
    ctx context.Context,
    call *streamtool.ToolCall,
) error {
    log.Printf(
        \"starting tool call: id=%s name=%s args=%s\",
        call.ID,
        call.Name,
        call.Arguments,
    )
    return nil
}

func (LoggingHook) AfterExecute(
    ctx context.Context,
    call *streamtool.ToolCall,
    result *streamtool.ToolResult,
) error {
    log.Printf(
        \"finished tool call: id=%s name=%s err=%v\",
        call.ID,
        call.Name,
        result.Err,
    )
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

如果注册多个 Hook：

```go
executor := streamtool.New(
    ctx,
    streamtool.WithHook(hookA),
    streamtool.WithHook(hookB),
)
```

执行顺序为：

```text
hookA.Before
    hookB.Before
        Tool.Execute
    hookB.After
hookA.After
```

这种栈式生命周期适合实现：

- logging
- tracing
- metrics
- audit
- timeout policy
- result transformation
- cache
- retry
- rate limiting

## 多工具并发

不同 `call_id` 相互独立。

例如模型同时生成：

```text
call_1 -> search
call_2 -> get_weather
call_3 -> get_profile
```

每个调用只要参数提前准备完成，都可以立即调度。

通过：

```go
streamtool.WithMaxConcurrency(8)
```

限制最大并发数：

```go
executor := streamtool.New(
    ctx,
    streamtool.WithMaxConcurrency(8),
)
```

准备完成但超过并发上限的工具会等待执行槽位，而不是无限制同时运行。

## ToolCallDelta

流式 Provider adapter 最终只需要转换到：

```go
type ToolCallDelta struct {
    ID             string
    NameDelta      string
    ArgumentsDelta string
    Done           bool
}
```

其中：

- `ID`：Tool Call 唯一 ID
- `NameDelta`：本次流事件携带的工具名增量
- `ArgumentsDelta`：参数字符串增量
- `Done`：Provider 已明确说明该 Tool Call 不会再产生更多参数

执行器不与 OpenAI、Anthropic 或其他具体 Provider 绑定。

Provider SDK 的 event 只需要在外层转换成 `ToolCallDelta`。

## 完整流式集成方式

典型 LLM streaming loop：

```go
executor := streamtool.New(
    ctx,
    streamtool.WithMaxConcurrency(8),
    streamtool.WithHook(tracingHook),
    streamtool.WithHook(metricsHook),
)

executor.Register(weatherTool, weatherSchema)
executor.Register(searchTool, searchSchema)

for event := range modelStream {
    // 普通文本仍然可以立即发送给客户端。
    if event.Text != \"\" {
        sendToClient(event.Text)
    }

    // Tool Call delta 同时不断推进 Executor。
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

// 模型 stream 已经结束。
// 此时一部分工具很可能早已运行结束，
// Collect 只需要等待仍未结束的工具。
results, err := executor.Collect(ctx)
if err != nil {
    return err
}

sendToolResultsBackToModel(results)
```

这里真正发生的时间线是：

```text
LLM
│
├─ text delta
├─ tool A args ...
├─ tool A args complete
│      └──────── Tool A starts
├─ more model tokens      Tool A running
├─ tool B args ...        Tool A running
├─ tool B complete
│      └──────── Tool B starts
├─ more model tokens      Tool A / B running
│
└─ stream EOF
       │
       └─ Collect()
              │
              └─ wait only for unfinished calls
```

## Collect

`Collect(ctx)` 同时承担两个职责。

### 1. Finalize

所有仍处于 Receiving 状态的调用进行最终检查：

```text
Tool unknown
    -> ErrUnknownTool

JSON incomplete
    -> ErrIncompleteArguments

Schema invalid
    -> ErrInvalidArguments

Valid
    -> schedule
```

### 2. Barrier

等待已经调度的所有 Tool 到达最终状态，并按 Tool Call 首次出现顺序返回：

```go
results, err := executor.Collect(ctx)
```

单个工具失败不会使整个 `Collect` 失败：

```go
for _, result := range results {
    if result.Err != nil {
        // 单独处理这个 Tool Call
    }
}
```

`Collect` 自身的 `error` 用于表示 Executor / Context 层面的失败。

## ToolResult

每个调用最终返回：

```go
type ToolResult struct {
    CallID string
    Name   string

    Value any
    Err   error

    StartedAt  time.Time
    FinishedAt time.Time
}
```

因此可以统计完整工具耗时：

```go
duration := result.FinishedAt.Sub(result.StartedAt)
```

## 错误类型

当前内置错误：

```go
ErrClosed
ErrIncompleteArguments
ErrUnknownTool
ErrInvalidArguments
```

推荐使用 `errors.Is` 判断：

```go
if errors.Is(result.Err, streamtool.ErrInvalidArguments) {
    // 参数不符合 Schema
}
```

不同 Tool Call 的业务执行错误保存在各自：

```go
ToolResult.Err
```

中。

## Exactly Once

同一个 `call_id` 一旦满足启动条件，就立即从 Receiving 切换到 Scheduled。

此后继续收到同一个 `call_id` 的 delta，也不会再次执行。

语义为：

```text
Receiving
    ↓
Scheduled
    ↓
Running
    ↓
Succeeded / Failed
```

关键不变量：

```text
每个 call_id 最多启动一次 Tool.Execute
```

## Context 生命周期

建议 Executor 使用覆盖整个 Tool Calling round 的 context：

```go
executor := streamtool.New(roundCtx)
```

不要因为 LLM streaming reader 已经 EOF，就提前取消 Executor context。

正确生命周期应为：

```text
Create Executor
      ↓
LLM Streaming
      ↓
Tools start while streaming
      ↓
LLM EOF
      ↓
Collect
      ↓
All tools finished
```

如果调用：

```go
results, err := executor.Collect(timeoutCtx)
```

则 `timeoutCtx` 控制等待 Collect barrier 的生命周期。

## 设计边界

当前版本刻意不做 speculative execution。

例如参数只有：

```text
{\"query\":\"hel
```

执行器不会猜测参数最终会变成什么，也不会提前执行工具。

只有当参数同时满足：

```text
完整 JSON + Schema Valid
```

才启动执行。

这样可以避免：

- 参数预测错误
- Tool 撤销
- 副作用补偿
- 重复写操作
- speculative task 一致性

第一版目标是重叠确定能够执行的工具与模型剩余生成时间，而不是预测模型未来要生成的参数。

## 本地验证

```bash
go test ./...
go vet ./...
```

项目测试目前覆盖核心语义：

- Push 后、Collect 前提前执行
- exactly-once
- Schema invalid 在流未结束时不过早失败
- Collect 收敛 incomplete JSON
- Hook stack ordering

## 项目结构

```text
.
├── call.go
├── errors.go
├── executor.go
├── executor_test.go
├── go.mod
├── go.sum
├── hook.go
├── option.go
├── registry.go
├── result.go
├── schema.go
├── tool.go
└── README.md
```

## License

当前仓库尚未添加 License。发布为公共 Go module 前，请根据项目实际开源策略补充 LICENSE。
