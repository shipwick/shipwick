package api

// volumeRoutes registers the endpoints of this feature. A backup carries the
// application's data and a restore replaces it, so both take admin.
func (s *Server) volumeRoutes(routes routeTable) {
	routes.read("GET /api/v1/applications/{name}/volumes", s.withName(s.handleListVolumes))
	routes.admin("GET /api/v1/applications/{name}/volumes/{volume}/archive", s.withName(s.handleBackup))
	routes.admin("PUT /api/v1/applications/{name}/volumes/{volume}/archive", s.withName(s.handleRestore))
}
