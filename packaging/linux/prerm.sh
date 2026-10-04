#!/bin/sh
# Runs before the shipwick-agent package is removed for good (not before an
# upgrade): the agent is stopped and no longer started at boot. The
# applications are containers of Docker and keep running, with nothing that
# supervises them.

set -e

if [ -d /run/systemd/system ]; then
    systemctl disable --now shipwick-agent.service >/dev/null 2>&1 || true
fi
