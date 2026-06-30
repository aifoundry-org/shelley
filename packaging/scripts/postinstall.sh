#!/bin/sh
set -e

# Create a dedicated system user to run the service.
if ! getent passwd shelley >/dev/null 2>&1; then
    useradd --system --home-dir /var/lib/shelley --shell /usr/sbin/nologin \
        --comment "Shelley coding agent" shelley
fi

install -d -o shelley -g shelley -m 0750 /var/lib/shelley

if [ -d /run/systemd/system ]; then
    systemctl daemon-reload || true
    systemctl enable shelley.socket >/dev/null 2>&1 || true
    # (Re)start via the socket so socket activation stays in charge.
    systemctl restart shelley.socket || true
    systemctl restart shelley.service || true
fi
