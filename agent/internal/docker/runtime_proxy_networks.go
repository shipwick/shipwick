package docker

import "context"

// MissingProxyNetworks names the networks of the installation the proxy's
// container is not attached to, in the order the compose file lists them:
// the application network, the services network, the control network. The
// proxy needs all three — the first two to reach replicas, the third to
// reach the agent — and a container whose start was refused once (a port
// below ip_unprivileged_port_start under rootless Docker) comes back on
// some of them only, and then serves some applications and not others.
//
// The list the daemon gives already carries each container's networks; no
// further call is made. ErrNoProxyContainer when the proxy is not a
// container of this daemon.
func (r *Runtime) MissingProxyNetworks(ctx context.Context) ([]string, error) {
	item, err := r.runningProxy(ctx)
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, name := range []string{r.network, r.services, ControlNetwork(r.network)} {
		if item.NetworkSettings == nil || item.NetworkSettings.Networks[name] == nil {
			missing = append(missing, name)
		}
	}
	return missing, nil
}
