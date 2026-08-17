package exec

import (
	"context"
)

// CommandExecutionContext holds values shared by all CLI commands. It can grow
// later without changing the Run method signature of every command.
type CommandExecutionContext struct {
	Context context.Context
}

// NewCommandExecutionContext is Go's usual constructor pattern. Go has no
// constructor keyword, so constructors are ordinary functions named NewX.
func NewCommandExecutionContext(ctx context.Context) *CommandExecutionContext {
	return &CommandExecutionContext{
		Context: ctx,
	}
}
