package streamingtoolexecutor

import (
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func CompileSchema(schema any) (*jsonschema.Schema, error) {
	if schema == nil {
		return nil, fmt.Errorf("compile schema: nil schema")
	}

	const location = "streaming-tool-executor://schema.json"
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(location, schema); err != nil {
		return nil, fmt.Errorf("compile schema: add resource: %w", err)
	}

	compiled, err := compiler.Compile(location)
	if err != nil {
		return nil, fmt.Errorf("compile schema: %w", err)
	}
	return compiled, nil
}
