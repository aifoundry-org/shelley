#!/bin/sh
set -e

if [ -d /run/systemd/system ]; then
    systemctl stop shelley.service || true
    systemctl stop shelley.socket || true
    systemctl disable shelley.socket >/dev/null 2>&1 || true
fi
