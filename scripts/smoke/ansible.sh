#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# ansible: ansible-galaxy collection build + publish to the Galaxy API v3, then
# collection install back. Image: ANSIBLE_IMAGE (default python:3.14-slim, the
# newest Python ansible-core supports; the newest ansible-core is pip installed).
# Usage: OA_TOKEN=... scripts/smoke/ansible.sh [URL]
# shellcheck source=lib.sh
. "$(dirname "$0")/lib.sh"
smoke_init ansible docker.io/library/python:3.14-slim "$@"

smoke_run "$(cat <<'SCRIPT'
set -eu
pip install -q ansible-core 2>&1 | tail -3
ansible-galaxy --version | head -1
cat > /tmp/ansible.cfg <<EOF
[galaxy]
server_list = openartifact

[galaxy_server.openartifact]
url = $OA_URL/repos/$REPO/api/
token = $OA_TOKEN
EOF
export ANSIBLE_CONFIG=/tmp/ansible.cfg
[ "$SMOKE_MODE" = install ] || {
  mkdir -p /tmp/p/src && cd /tmp/p/src
  cat > galaxy.yml <<EOF
namespace: smoke
name: $PKG
version: $VERSION
readme: README.md
authors:
  - smoke
EOF
  echo "marker $PKG" > README.md
  ansible-galaxy collection build --output-path /tmp/p >/dev/null
  ansible-galaxy collection publish "/tmp/p/smoke-$PKG-$VERSION.tar.gz" --server openartifact
}
[ "$SMOKE_MODE" = publish ] || {
  # fresh ansible state: its own home (response cache, collections cache) and install directory
  export ANSIBLE_HOME=/tmp/ansible-home-install
  ansible-galaxy collection install "smoke.$PKG:$VERSION" --server openartifact -p /tmp/c
  [ "$(cat "/tmp/c/ansible_collections/smoke/$PKG/README.md")" = "marker $PKG" ]
}
SCRIPT
)"
