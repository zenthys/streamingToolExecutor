package streamingtoolexecutor

import (
	"fmt"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

type toolEntry struct {
	tool   Tool
	schema *jsonschema.Schema
}

type Registry struct {
	mu    sync.RWMutex
	tools map[string]*toolEntry
}

func newRegistry() *Registry {
	return &Registry{tools: make(map[string]*toolEntry)}
}

func (r *Registry) register(tool Tool, schema *jsonschema.Schema) error {
	if tool == nil {
		return fmt.Errorf("register tool: nil tool")
	}
	if tool.Name() == "" {
		return fmt.Errorf("register tool: empty name")
	}
	if schema == nil {
		return fmt.Errorf("register tool %q: nil schema", tool.Name())
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.tools[tool.Name()]; exists {
		return fmt.Errorf("register tool %q: already registered", tool.Name())
	}
	r.tools[tool.Name()] = &toolEntry{tool: tool, schema: schema}
	return nil
}

func (r *Registry) get(name string) (*toolEntry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.tools[name]
	return entry, ok
}
