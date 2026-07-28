package handler

import "fmt"

const (
	defaultAgentMaxConcurrentTasks int32 = 6
	minAgentMaxConcurrentTasks     int32 = 1
	maxAgentMaxConcurrentTasks     int32 = 50
)

func validateAgentMaxConcurrentTasks(value int32) error {
	if value < minAgentMaxConcurrentTasks || value > maxAgentMaxConcurrentTasks {
		return fmt.Errorf(
			"max_concurrent_tasks must be between %d and %d",
			minAgentMaxConcurrentTasks,
			maxAgentMaxConcurrentTasks,
		)
	}
	return nil
}
