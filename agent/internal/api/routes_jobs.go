package api

// jobRoutes registers the endpoints of this feature.
func (s *Server) jobRoutes(routes routeTable) {
	routes.read("GET /api/v1/applications/{name}/jobs", s.withName(s.handleJobs))
	routes.read("GET /api/v1/applications/{name}/runs", s.withName(s.handleRuns))
	routes.read("GET /api/v1/applications/{name}/runs/{id}", s.withName(s.handleRun))
	// Starting a container from the application's image is deploying, in
	// effect; and a command can do anything the application's credentials
	// allow.
	routes.deploy("POST /api/v1/applications/{name}/jobs/{job}/run", s.withName(s.handleRunJob))
	routes.deploy("POST /api/v1/applications/{name}/run", s.withName(s.handleRunCommand))
}
