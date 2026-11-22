#!/bin/sh
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later

# pre-remove script of the deb and rpm server packages (nfpm runs it with the
# packager's convention: rpm passes 0 on erase and 1 on upgrade, deb passes
# "remove" on removal and "upgrade" on upgrade).
set -eu

if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
  case "$1" in
    0|remove)
      systemctl disable --now openartifact.service >/dev/null 2>&1 || true
      ;;
  esac

  systemctl daemon-reload >/dev/null 2>&1 || true
fi
