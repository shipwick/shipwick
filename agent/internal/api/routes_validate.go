package api

// validateRoutes registers the dry run of a deploy.yaml. It changes nothing,
// but it answers with what the server holds — which hostnames and ports are
// taken, which secrets are stored — to whoever could deploy the document
// anyway: the same role.
func (s *Server) validateRoutes(routes routeTable) {
	routes.deploy("POST /api/v1/applications/{name}/validate", s.withName(s.handleValidate))
}
