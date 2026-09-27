package api

// secretRoutes registers the secret endpoints. Anyone who may read sees the
// names — a deploy.yaml refers to them, and `shipwick validate` may want to
// say which are there; setting and removing values is admin's, like tokens.
func (s *Server) secretRoutes(routes routeTable) {
	routes.read("GET /api/v1/secrets", s.handleListSecrets)
	routes.admin("PUT /api/v1/secrets/{name}", s.handleSetSecret)
	routes.admin("DELETE /api/v1/secrets/{name}", s.handleDeleteSecret)
}
