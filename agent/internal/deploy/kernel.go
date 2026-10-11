package deploy

import "github.com/shipwick/shipwick/pkg/api"

// kernelLimits is what GET /server says about the kernel's limits: the
// neighbour table where the agent can read it, and nothing where it cannot.
// A client reads nothing into its absence — an agent in a container, which
// is most of them, has no view of the host's setting.
func (e *Engine) kernelLimits() *api.KernelLimits {
	if e.opts.NeighbourTable == nil {
		return nil
	}
	entries, ok := e.opts.NeighbourTable()
	if !ok {
		return nil
	}
	return &api.KernelLimits{NeighbourTable: entries}
}
