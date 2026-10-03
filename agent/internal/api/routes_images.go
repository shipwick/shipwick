package api

// imageRoutes registers the endpoints of this feature. Sending an image is
// half of a deployment, so it takes the same role; so does asking which of
// its layers need sending.
func (s *Server) imageRoutes(routes routeTable) {
	routes.deploy("POST /api/v1/applications/{name}/images", s.withName(s.handleLoadImage))
	routes.deploy("POST /api/v1/applications/{name}/images/missing", s.withName(s.handleMissingLayers))
}
