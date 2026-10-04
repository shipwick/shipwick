#!/bin/sh
# Runs after the files of the shipwick-agent package are in place: the
# postinst of the .deb and the post-install scriptlet of the .rpm. First
# argument: "install" or "upgrade".
#
# It writes the settings file with a token of its own on the first
# installation and never touches it again, and it starts nothing: the agent
# needs Docker, and whoever installs it decides when it runs. An agent that
# is running is restarted, so that it is the version just installed.

set -e

ENV_FILE=/etc/shipwick/agent.env

if [ ! -e "$ENV_FILE" ]; then
    token="$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')"
    [ "${#token}" -eq 64 ] || { echo "shipwick-agent: could not generate a token" >&2; exit 1; }
    # The token is root on this server: the file is created unreadable to
    # anyone else, not made so afterwards.
    ( umask 077
      mkdir -p /etc/shipwick
      sed "s/^SHIPWICK_AGENT_TOKEN=\$/SHIPWICK_AGENT_TOKEN=$token/" /usr/share/shipwick/agent.env > "$ENV_FILE" )
fi

# Not in a container being built, nor on a host with another init.
if [ -d /run/systemd/system ]; then
    systemctl daemon-reload || true
    if [ "$1" = "upgrade" ]; then
        systemctl try-restart shipwick-agent.service || true
    fi
fi

if [ "$1" != "upgrade" ]; then
    cat <<EOF

The Shipwick agent is installed and not started.

  1. It needs Docker Engine on this server (https://docs.docker.com/engine/install/).
  2. Its settings, with a generated API token:  $ENV_FILE
  3. Start it now and at boot:                   systemctl enable --now shipwick-agent
  4. The reverse proxy and the dashboard run as containers:
       docker compose --env-file $ENV_FILE -f /usr/share/shipwick/compose.yml up -d

What this package covers, and what it does not: handbook §4, "Installation from a package".

EOF
fi
