#!/bin/sh
set -e

if [ -d /run/systemd/system ]; then
    systemctl daemon-reload || true
fi

# On purge, drop state and the service user. `remove` keeps them so an
# upgrade/reinstall preserves the database.
if [ "$1" = "purge" ]; then
    rm -rf /var/lib/shelley
    if getent passwd shelley >/dev/null 2>&1; then
        userdel shelley || true
    fi
fi
