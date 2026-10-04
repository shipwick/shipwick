package commands

import "github.com/shipwick/shipwick/pkg/api"

// renderApplicationCaller is what a command says when it ran inside the
// container of an application and went to the agent directly.
func renderApplicationCaller() string {
	return "The agent does not answer the containers of applications: whatever runs in one could otherwise try tokens against it.\n\n" +
		"Run the command outside the container, or reach the server at its hostname: shipwick login --url https://<SHIPWICK_AGENT_DOMAIN>"
}

// reach says when nothing but the token stands between an application's
// container and the API. An agent before 0.8 never says, and nothing is read
// into its silence.
func (r *report) reach(info api.Server) {
	if info.OpenToApplications {
		r.hint("Application containers can reach the agent's API: it listens on a network they are on, and only the token keeps them out. " +
			"On the server, run the installer again; if this stays, the agent's log says why, and \"Who can reach the API\" in the handbook (Security) what to change")
	}
}
