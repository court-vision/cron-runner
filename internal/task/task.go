package task

import "context"

// Task is any unit of work the scheduler can execute.
// Implementing this interface is all that's needed to schedule any job —
// pipeline triggers, loops, deployments, test runs, etc.
type Task interface {
	Name() string
	Run(ctx context.Context) error
}
