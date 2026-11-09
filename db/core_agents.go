package db

// IsCoreAgent identifies the five roles exposed by the simplified workbench.
// Historical agents remain stored so their conversations and bindings are not lost.
func IsCoreAgent(key string) bool {
	switch key {
	case "goals", "planner", "mainagent", "worker", "reporter":
		return true
	default:
		return false
	}
}
