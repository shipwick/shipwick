package commands

import (
	"fmt"

	"github.com/shipwick/shipwick/cli/internal/ui"
	"github.com/shipwick/shipwick/pkg/version"
)

// versionGap is the sentence `server status` adds when the CLI and the agent
// are not of the same minor version: the two are released together, and a
// command the older one does not know fails with a word about it, so the
// sentence says which side to bring up to date and how. Patch releases
// differ without a word, and a development build of either side says
// nothing: it is not a release to compare.
func (c *cli) versionGap(agent string) string {
	mine, ok := version.Parse(version.Version)
	theirs, okAgent := version.Parse(agent)
	if !ok || !okAgent || (mine.Major == theirs.Major && mine.Minor == theirs.Minor) {
		return ""
	}
	if version.Compare(version.Version, agent) > 0 {
		return c.ui.Styled(ui.Yellow, "!") + fmt.Sprintf(" The CLI (%s) is newer than the agent (%s); upgrade the server with: %s", version.Version, agent, installerCommand)
	}
	return c.ui.Styled(ui.Yellow, "!") + fmt.Sprintf(" The CLI (%s) is older than the agent (%s); upgrade it with: shipwick upgrade", version.Version, agent)
}
