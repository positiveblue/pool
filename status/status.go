package status

// Default statuses.
const (
	StartingUp            = "starting_up"
	WaitingLeaderElection = "waiting_leader_election"
	UpAndRunning          = "up_and_running"
	ShuttingDown          = "shutting_down"
	Unknown               = "unknown"
)

// DefaultIsAlive is the IsAlive function used in the DefaultConfiguration.
func DefaultIsAlive(status string) bool {
	switch status {
	case StartingUp, WaitingLeaderElection, UpAndRunning, ShuttingDown:
		return true
	default:
		return false
	}
}

// DefaultIsReady is the IsReady function used in the DefaultConfiguration.
func DefaultIsReady(status string) bool {
	switch status {
	case UpAndRunning:
		return true
	default:
		return false
	}
}

// DefaultIsValidStatus is the isValidStatus function used in the
// DefaultConfiguration.
func DefaultIsValidStatus(status string) bool {
	switch status {
	case StartingUp, WaitingLeaderElection, UpAndRunning, ShuttingDown, Unknown:
		return true
	default:
		return false
	}
}
