package site

// QueuesSettled keeps approval detail visible until requested grants are applied.
func (e Enrollment) QueuesSettled() bool {
	if e.Status != "active" {
		return false
	}
	requested := e.Queues
	if len(requested) == 0 {
		requested = []string{"chickadee"}
	}
	if len(requested) != len(e.EnabledQueues) {
		return false
	}
	enabled := map[string]bool{}
	for _, queue := range e.EnabledQueues {
		enabled[queue] = true
	}
	for _, queue := range requested {
		if !enabled[queue] {
			return false
		}
	}
	return true
}
