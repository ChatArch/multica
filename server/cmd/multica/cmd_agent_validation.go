package main

import "fmt"

const (
	minAgentMaxConcurrentTasks int32 = 1
	maxAgentMaxConcurrentTasks int32 = 50
)

func validateAgentMaxConcurrentTasksFlag(value int32) error {
	if value < minAgentMaxConcurrentTasks || value > maxAgentMaxConcurrentTasks {
		return fmt.Errorf(
			"--max-concurrent-tasks must be between %d and %d (got %d)",
			minAgentMaxConcurrentTasks,
			maxAgentMaxConcurrentTasks,
			value,
		)
	}
	return nil
}
