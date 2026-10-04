package upstreams

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/caddyserver/caddy/v2"
)

func init() {
	caddy.RegisterModule(Admin{})
}

// Admin adds Shipwick's one endpoint to Caddy's admin API.
//
// The source keeps what a name resolved to for a second, and for two while
// Docker's DNS is silent. That is how long the address of a replica that has
// stopped may still be used for its application, and so how long the agent
// keeps it from the containers of every other one. The agent stops most
// replicas itself; it says so here, the names are dropped, and the address is
// free without the wait.
type Admin struct{}

// CaddyModule returns the Caddy module information.
func (Admin) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "admin.api.shipwick",
		New: func() caddy.Module { return new(Admin) },
	}
}

// forgetPath takes a POST of {"names": [...]} and answers 200 once none of
// the names has an answer older than the request.
const forgetPath = "/shipwick/forget"

// Routes returns the endpoint.
func (Admin) Routes() []caddy.AdminRoute {
	return []caddy.AdminRoute{{Pattern: forgetPath, Handler: forgetHandler(names)}}
}

type forgetRequest struct {
	Names []string `json:"names"`
}

func forgetHandler(t *table) caddy.AdminHandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		if r.Method != http.MethodPost {
			return caddy.APIError{HTTPStatus: http.StatusMethodNotAllowed, Err: errors.New("method not allowed")}
		}
		var req forgetRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			return caddy.APIError{HTTPStatus: http.StatusBadRequest, Err: fmt.Errorf("reading the names: %w", err)}
		}
		t.drop(req.Names)
		// The agent takes nothing but this for a confirmation: any other
		// answer, an older proxy's 404 among them, leaves the wait in place.
		w.WriteHeader(http.StatusOK)
		return nil
	}
}

var _ caddy.AdminRouter = Admin{}
