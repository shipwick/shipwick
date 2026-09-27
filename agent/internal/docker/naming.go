package docker

import (
	"fmt"
	"strconv"
)

// Labels identify Shipwick-managed containers. Labels, not names, are the
// source of truth when the agent looks for its containers: names are for
// humans reading `docker ps`.
const (
	LabelManaged    = "com.shipwick.managed"
	LabelApp        = "com.shipwick.app"
	LabelDeployment = "com.shipwick.deployment"
	LabelReplica    = "com.shipwick.replica"
)

// ContainerName implements the naming convention
//
//	shipwick_<app>_<deployment sequence>_<replica>
//
// e.g. shipwick_my-api_7_1. Application names cannot contain underscores, so
// the name is unambiguous.
func ContainerName(app string, sequence, replica int) string {
	return fmt.Sprintf("shipwick_%s_%d_%d", app, sequence, replica)
}

func containerLabels(app string, deploymentID int64, replica int) map[string]string {
	return map[string]string{
		LabelManaged:    "true",
		LabelApp:        app,
		LabelDeployment: strconv.FormatInt(deploymentID, 10),
		LabelReplica:    strconv.Itoa(replica),
	}
}

// Labels of a one-off job container. It carries LabelManaged and LabelApp
// like a replica — it is the agent's to clean up — but LabelJob instead of
// LabelReplica, so that everything that manages replicas can skip it.
const (
	LabelJob = "com.shipwick.job"
	LabelRun = "com.shipwick.run"
)

// JobContainerName names a one-off container:
//
//	shipwick_<app>_job_<job>_<run id>
//
// e.g. shipwick_my-api_job_nightly-report_42. Run IDs are never reused, so
// the name is unique for the life of the server.
func JobContainerName(app, job string, runID int64) string {
	return fmt.Sprintf("shipwick_%s_job_%s_%d", app, job, runID)
}
