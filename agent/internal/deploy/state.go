package deploy

import "github.com/shipwick/shipwick/pkg/api"

// transitions is the deployment state machine.
//
//	PENDING → BUILDING → STARTING → HEALTH_CHECKING → HEALTHY → ACTIVE → SUPERSEDED
//
// Any state before ACTIVE may fail. A failed deployment that already disturbed
// the previous version is rolled back:
//
//	FAILED → ROLLBACK → RESTORING → ROLLED_BACK
//
// after which the previous deployment is ACTIVE again. BUILDING covers
// obtaining the image; Shipwick pulls images, it does not build them.
var transitions = map[api.DeploymentStatus][]api.DeploymentStatus{
	api.StatusPending:        {api.StatusBuilding, api.StatusFailed},
	api.StatusBuilding:       {api.StatusStarting, api.StatusFailed},
	api.StatusStarting:       {api.StatusHealthChecking, api.StatusFailed},
	api.StatusHealthChecking: {api.StatusHealthy, api.StatusFailed},
	api.StatusHealthy:        {api.StatusActive, api.StatusFailed},
	api.StatusActive:         {api.StatusSuperseded},
	api.StatusFailed:         {api.StatusRollback},
	api.StatusRollback:       {api.StatusRestoring, api.StatusFailed},
	api.StatusRestoring:      {api.StatusRolledBack, api.StatusFailed},
	api.StatusSuperseded:     nil,
	api.StatusRolledBack:     nil,
}

// CanTransition reports whether the state machine allows from → to.
func CanTransition(from, to api.DeploymentStatus) bool {
	for _, next := range transitions[from] {
		if next == to {
			return true
		}
	}
	return false
}

// IsSettled reports whether s is an outcome rather than a phase of work. Note
// that the engine may still be cleaning up right after a deployment settles
// (retiring the old version, discarding failed containers); completed_at, not
// the status, marks the moment the engine is done with a deployment.
func IsSettled(s api.DeploymentStatus) bool {
	switch s {
	case api.StatusActive, api.StatusSuperseded, api.StatusFailed, api.StatusRolledBack:
		return true
	}
	return false
}

// InFlightStatuses lists every status in which the engine is actively working
// on a deployment. A deployment found in one of these at agent startup was
// interrupted by a crash or restart.
func InFlightStatuses() []api.DeploymentStatus {
	var out []api.DeploymentStatus
	for s := range transitions {
		if !IsSettled(s) {
			out = append(out, s)
		}
	}
	return out
}
