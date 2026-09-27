package api

// staticRoutes registers the endpoints of this feature. Uploading a folder
// changes what a domain serves only through the deployment that follows, and
// takes the same role.
func (s *Server) staticRoutes(routes routeTable) {
	routes.deploy("PUT /api/v1/applications/{name}/static", s.withName(s.handleUploadStatic))
}
