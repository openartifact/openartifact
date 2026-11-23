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
# backup.backupDir); install -d creates them or resets their owner and mode.
install -d -o openartifact -g openartifact -m 0750 \
  /var/lib/artifacts /var/lib/openartifact /var/lib/openartifact/backups

# The config holds the database password: root-owned, readable by the server's
# group only. The package ships it 0640 root:root (the group does not exist
# when the files are unpacked), so it gets the group here, on every install and
# upgrade, since an upgrade replaces a config the operator did not edit.
chgrp openartifact /etc/openartifact/openartifact.yaml

# OPENARTIFACT_SECRET_KEY encrypts upstream passwords, webhook secrets and
# signing keys at rest and must never change, so it is generated once and the
# file is not part of the package (removing the package keeps it).
env_file=/etc/openartifact/openartifact.env
if [ ! -e "$env_file" ]; then
  key="$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')"
  (umask 027 && printf 'OPENARTIFACT_SECRET_KEY=%s\n' "$key" >"$env_file")
  chgrp openartifact "$env_file"
fi

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
  # a fresh install is neither enabled nor started: the operator does both
  # after editing the config.
  if [ "$upgrading" = true ]; then
    if systemctl is-active --quiet openartifact.service; then
      systemctl restart openartifact.service >/dev/null 2>&1 || true
    fi
  fi
fi
