package api

// metricsRoutes registers the endpoints of this feature.
func (s *Server) metricsRoutes(routes routeTable) {
	routes.read("GET /api/v1/applications/{name}/metrics/history", s.withName(s.handleMetricsHistory))
}
