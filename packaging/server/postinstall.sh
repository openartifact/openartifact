#!/bin/sh
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later

# post-install script of the deb and rpm server packages (nfpm runs it with the
# packager's convention: rpm passes 1 on install and 2 on upgrade, deb passes
# "configure" plus the previously configured version on upgrade).
set -eu

if ! getent passwd openartifact >/dev/null 2>&1; then
  useradd --system --user-group \
    --home-dir /var/lib/openartifact --shell /usr/sbin/nologin \
    --comment "OpenArtifact server" openartifact
fi

# State directories of the shipped config defaults (storage.path and
# backup.backupDir); install -d is a no-op on existing directories.
install -d -o openartifact -g openartifact -m 0750 \
  /var/lib/artifacts /var/lib/openartifact /var/lib/openartifact/backups

if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
  systemctl daemon-reload >/dev/null 2>&1 || true

  upgrading=false
  case "$1" in
    2) upgrading=true ;;
    configure)
      if [ -n "${2:-}" ]; then
        upgrading=true
      fi
      ;;
  esac

  # Upgrades keep a running server up (restart to pick up the new binary);
  # a fresh install is only enabled, never started, by the operator.
  if [ "$upgrading" = true ]; then
    if systemctl is-active --quiet openartifact.service; then
      systemctl restart openartifact.service >/dev/null 2>&1 || true
    fi
  fi
fi
