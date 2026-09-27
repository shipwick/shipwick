package api

// volumeRoutes registers the endpoints of this feature. A backup carries the
// application's data and a restore replaces it, so both take admin.
func (s *Server) volumeRoutes(routes routeTable) {
	routes.read("GET /api/v1/applications/{name}/volumes", s.withName(s.handleListVolumes))
	routes.admin("GET /api/v1/applications/{name}/volumes/{volume}/archive", s.withName(s.handleBackup))
	routes.admin("PUT /api/v1/applications/{name}/volumes/{volume}/archive", s.withName(s.handleRestore))
}

// managedVolumeRoutes registers the server-wide volume listing: every volume
// Shipwick created, the ones whose application was deleted included, and the
// removal of those. Removing data takes admin.
func (s *Server) managedVolumeRoutes(routes routeTable) {
	routes.read("GET /api/v1/volumes", s.handleListManagedVolumes)
	routes.admin("DELETE /api/v1/volumes/{name}", s.handleRemoveManagedVolume)
}
