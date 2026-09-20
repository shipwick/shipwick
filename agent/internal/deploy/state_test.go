package deploy

import (
	"testing"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/pkg/api"
)

func containerSpec(app string, deploymentID int64, sequence, replica int) docker.ContainerSpec {
	return docker.ContainerSpec{App: app, DeploymentID: deploymentID, Sequence: sequence, Replica: replica, Image: app + ":test"}
}

func TestHappyPathIsLegal(t *testing.T) {
	path := []api.DeploymentStatus{
		api.StatusPending, api.StatusBuilding, api.StatusStarting,
		api.StatusHealthChecking, api.StatusHealthy, api.StatusActive, api.StatusSuperseded,
	}
	for i := 0; i < len(path)-1; i++ {
		if !CanTransition(path[i], path[i+1]) {
			t.Errorf("%s → %s should be legal", path[i], path[i+1])
		}
	}
}

func TestRollbackPathIsLegal(t *testing.T) {
	path := []api.DeploymentStatus{api.StatusFailed, api.StatusRollback, api.StatusRestoring, api.StatusRolledBack}
	for i := 0; i < len(path)-1; i++ {
		if !CanTransition(path[i], path[i+1]) {
			t.Errorf("%s → %s should be legal", path[i], path[i+1])
		}
	}
}

func TestEveryInFlightStateCanFail(t *testing.T) {
	for _, s := range InFlightStatuses() {
		if !CanTransition(s, api.StatusFailed) {
			t.Errorf("%s → FAILED must be legal, or an interrupted deployment could never be settled", s)
		}
	}
}

func TestIllegalTransitions(t *testing.T) {
	illegal := [][2]api.DeploymentStatus{
		{api.StatusPending, api.StatusActive},         // no skipping health checks
		{api.StatusPending, api.StatusStarting},       // no skipping the image
		{api.StatusStarting, api.StatusHealthy},       // no skipping health checks
		{api.StatusHealthChecking, api.StatusActive},  // must be HEALTHY first
		{api.StatusActive, api.StatusFailed},          // an active deployment is superseded, never failed
		{api.StatusActive, api.StatusPending},         // records are immutable: no restarts
		{api.StatusSuperseded, api.StatusActive},      // rollback creates a new deployment instead
		{api.StatusFailed, api.StatusActive},          //
		{api.StatusFailed, api.StatusPending},         //
		{api.StatusRolledBack, api.StatusActive},      //
		{api.StatusHealthy, api.StatusHealthChecking}, // no going backwards
		{api.StatusPending, api.StatusPending},        // no self loops
		{"BOGUS", api.StatusFailed},
	}
	for _, tr := range illegal {
		if CanTransition(tr[0], tr[1]) {
			t.Errorf("%s → %s should be illegal", tr[0], tr[1])
		}
	}
}

func TestSettledAndInFlightPartitionAllStates(t *testing.T) {
	inFlight := map[api.DeploymentStatus]bool{}
	for _, s := range InFlightStatuses() {
		inFlight[s] = true
	}
	for s := range transitions {
		if IsSettled(s) == inFlight[s] {
			t.Errorf("%s must be exactly one of settled / in-flight", s)
		}
	}
	for _, s := range []api.DeploymentStatus{api.StatusActive, api.StatusSuperseded, api.StatusFailed, api.StatusRolledBack} {
		if !IsSettled(s) {
			t.Errorf("%s should be settled", s)
		}
	}
}

func TestApplicationStatus(t *testing.T) {
	tests := []struct {
		name      string
		hasActive bool
		desired   string
		replicas  api.ReplicaCount
		deploying bool
		crashLoop bool
		want      api.ApplicationStatus
	}{
		{"first deploy in flight", false, api.DesiredRunning, api.ReplicaCount{}, true, false, api.AppDeploying},
		{"never deployed successfully", false, api.DesiredRunning, api.ReplicaCount{}, false, false, api.AppFailed},
		{"all replicas healthy", true, api.DesiredRunning, api.ReplicaCount{Desired: 2, Running: 2, Healthy: 2}, false, false, api.AppHealthy},
		{"redeploy in flight keeps serving", true, api.DesiredRunning, api.ReplicaCount{Desired: 2, Running: 2, Healthy: 2}, true, false, api.AppHealthy},
		{"one replica down", true, api.DesiredRunning, api.ReplicaCount{Desired: 2, Running: 1, Healthy: 1}, false, false, api.AppDegraded},
		{"one replica running but unhealthy", true, api.DesiredRunning, api.ReplicaCount{Desired: 2, Running: 2, Healthy: 1}, false, false, api.AppDegraded},
		{"all running, none healthy", true, api.DesiredRunning, api.ReplicaCount{Desired: 2, Running: 2, Healthy: 0}, false, false, api.AppDown},
		{"nothing running", true, api.DesiredRunning, api.ReplicaCount{Desired: 2}, false, false, api.AppDown},
		{"crash loop outranks degraded", true, api.DesiredRunning, api.ReplicaCount{Desired: 2, Running: 1, Healthy: 1}, false, true, api.AppCrashLoop},
		{"crash loop outranks down", true, api.DesiredRunning, api.ReplicaCount{Desired: 1}, false, true, api.AppCrashLoop},
		{"stopped on request", true, api.DesiredStopped, api.ReplicaCount{Desired: 2}, false, false, api.AppStopped},
		{"stopped outranks a stale crash loop", true, api.DesiredStopped, api.ReplicaCount{Desired: 2}, false, true, api.AppStopped},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := applicationStatus(tt.hasActive, tt.desired, tt.replicas, tt.deploying, tt.crashLoop); got != tt.want {
				t.Errorf("got %s, want %s", got, tt.want)
			}
		})
	}
}
