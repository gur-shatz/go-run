package runctl

import "fmt"

type phaseHealth string

const (
	phasePending phaseHealth = "pending"
	phaseHealthy phaseHealth = "healthy"
	phaseFailed  phaseHealth = "failed"
)

// HeartbeatSummary is the aggregated console heartbeat state for selected targets.
type HeartbeatSummary struct {
	BuildFailures int
	RunFailures   int
	TestFailures  int
	AllHealthy    bool
}

func phaseStatus(t TargetStatus, phaseKey string) phaseHealth {
	if t.State == StateError && t.CurrentStage == phaseKey {
		return phaseFailed
	}
	if phaseKey == "test" && t.State == StateError && t.CurrentStage == "build" {
		return phaseFailed
	}
	if t.CurrentStage == phaseKey {
		return phasePending
	}

	var result string
	switch phaseKey {
	case "build":
		result = t.Build.Result
	case "test":
		result = t.Test.Result
	default:
		return phasePending
	}

	switch result {
	case "failed":
		return phaseFailed
	case "success":
		return phaseHealthy
	default:
		return phasePending
	}
}

func runStatus(t TargetStatus) phaseHealth {
	switch t.State {
	case StateError, StateExited:
		return phaseFailed
	case StateRunning:
		return phaseHealthy
	default:
		return phasePending
	}
}

// SummarizeHeartbeat returns the pessimistic build/run/test aggregate for console output.
// If selected is empty, all enabled targets are included.
func SummarizeHeartbeat(statuses []TargetStatus, selected map[string]bool) HeartbeatSummary {
	summary := HeartbeatSummary{AllHealthy: true}

	for _, t := range statuses {
		if len(selected) > 0 && !selected[t.Name] {
			continue
		}
		if len(selected) == 0 && !t.Enabled {
			continue
		}

		if t.HasBuild {
			switch phaseStatus(t, "build") {
			case phaseFailed:
				summary.BuildFailures++
				summary.AllHealthy = false
			case phasePending:
				summary.AllHealthy = false
			}
		}

		if t.HasRun {
			switch runStatus(t) {
			case phaseFailed:
				summary.RunFailures++
				summary.AllHealthy = false
			case phasePending:
				summary.AllHealthy = false
			}
		}

		if t.HasTest {
			switch phaseStatus(t, "test") {
			case phaseFailed:
				summary.TestFailures++
				summary.AllHealthy = false
			case phasePending:
				summary.AllHealthy = false
			}
		}
	}

	return summary
}

func (s HeartbeatSummary) HasFailures() bool {
	return s.BuildFailures > 0 || s.RunFailures > 0 || s.TestFailures > 0
}

func (s HeartbeatSummary) FailureTuple() string {
	return fmt.Sprintf("(%d %d %d)", s.BuildFailures, s.RunFailures, s.TestFailures)
}
