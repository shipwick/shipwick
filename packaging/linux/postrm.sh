#!/bin/sh
# Runs after the shipwick-agent package was removed. First argument: "purge"
# when the settings go too (apt purge); an .rpm never purges.
#
# /var/lib/shipwick is left where it is, always: it holds the database and
# the key that decrypts it, and the containers and volumes it describes are
# still on this server.

set -e

if [ -d /run/systemd/system ]; then
    systemctl daemon-reload || true
fi

if [ "$1" = "purge" ]; then
    rm -f /etc/shipwick/agent.env
    rmdir /etc/shipwick 2>/dev/null || true
    if [ -d /var/lib/shipwick ]; then
        echo "shipwick-agent: /var/lib/shipwick was kept: the agent's database and encryption key. Remove it yourself when the server's applications are gone."
    fi
fi
