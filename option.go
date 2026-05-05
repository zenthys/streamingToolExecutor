package streamingtoolexecutor

type Option func(*Executor)

func WithMaxConcurrency(n int) Option {
	return func(e *Executor) {
		if n > 0 {
			e.maxConcurrency = n
		}
	}
}

func WithHook(h Hook) Option {
	return func(e *Executor) {
		if h != nil {
			e.hooks = append(e.hooks, h)
		}
	}
}
