package commands

import (
	"github.com/shipwick/shipwick/cli/internal/ui"
	"github.com/shipwick/shipwick/pkg/api"
)

// describeAgent is the Agent row of `server status`: the version, and the
// newer release when the agent knows of one. The agent asks GitHub itself,
// once a day; an agent from before 0.7, one told not to ask and one that
// cannot reach GitHub say nothing, and neither does this row.
func (c *cli) describeAgent(info api.Server) string {
	if info.Update == nil || !info.Update.Available {
		return info.AgentVersion
	}
	return info.AgentVersion + "  " + c.ui.Styled(ui.Yellow, info.Update.LatestVersion+" is available") +
		c.ui.Styled(ui.Dim, "  (on the server, run the installer again: "+installerCommand+")")
}
