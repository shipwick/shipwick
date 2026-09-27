package api

import (
	"net/http"
	"strconv"

	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

const (
	defaultRunLimit = 50
	maxRunLimit     = 500
)

func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request, name string) {
	jobs, err := s.engine.Jobs(r.Context(), name)
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, jobs)
}

// handleRuns lists runs newest first, without their output; ?job= narrows
// them to one job, "pre-deploy" or "run" included.
func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request, name string) {
	limit, ok := intParam(w, r, "limit", defaultRunLimit, 1, maxRunLimit)
	if !ok {
		return
	}
	job := r.URL.Query().Get("job")
	if job != "" {
		if err := spec.ValidateJobName(job); err != nil {
			writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, err.Error(), nil)
			return
		}
	}
	runs, err := s.engine.Runs(r.Context(), name, job, limit)
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request, name string) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "run id must be a positive number", nil)
		return
	}
	run, err := s.engine.JobRun(r.Context(), name, id)
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) handleRunJob(w http.ResponseWriter, r *http.Request, name string) {
	job := r.PathValue("job")
	if err := spec.ValidateJobName(job); err != nil {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, err.Error(), nil)
		return
	}
	run, err := s.engine.RunJob(r.Context(), name, job)
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	s.acceptRun(w, run)
}

// handleRunCommand runs a one-off command: body {"command": ["...", ...]}.
// The command is validated like one from deploy.yaml; the body never reaches
// a shell, the argv goes to the container as it is.
func (s *Server) handleRunCommand(w http.ResponseWriter, r *http.Request, name string) {
	var req api.RunRequest
	if !s.decodeOptionalBody(w, r, &req) {
		return
	}
	if err := spec.ValidateCommand(req.Command); err != nil {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "command "+err.Error(), nil)
		return
	}
	run, err := s.engine.RunCommand(r.Context(), name, req.Command)
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	s.acceptRun(w, run)
}

// acceptRun answers 202: the container runs in the background; poll the
// Location until finished_at is set.
func (s *Server) acceptRun(w http.ResponseWriter, run store.JobRun) {
	w.Header().Set("Location", "/api/v1/applications/"+run.Application+"/runs/"+strconv.FormatInt(run.ID, 10))
	writeJSON(w, http.StatusAccepted, api.RunDetail{Run: api.Run{
		ID:           run.ID,
		Application:  run.Application,
		Job:          run.Job,
		Kind:         run.Kind,
		Command:      run.Command,
		Status:       run.Status,
		ExitCode:     run.ExitCode,
		DeploymentID: run.DeploymentID,
		StartedAt:    run.StartedAt,
		FinishedAt:   run.FinishedAt,
	}})
}
