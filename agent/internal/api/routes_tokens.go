package api

// tokenRoutes registers the token endpoints. A token is what grants access,
// so managing them is the most sensitive thing the API does: admin only.
func (s *Server) tokenRoutes(routes routeTable) {
	routes.admin("GET /api/v1/tokens", s.handleListTokens)
	routes.admin("POST /api/v1/tokens", s.handleCreateToken)
	routes.admin("DELETE /api/v1/tokens/{name}", s.handleRevokeToken)
	routes.admin("PUT /api/v1/tokens/{name}", s.handleUpdateToken)
}
