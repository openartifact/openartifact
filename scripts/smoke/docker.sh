#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# docker: docker login, tag, push, remove, pull and run an image through /v2.
# Image: DOCKER_IMAGE (default docker.io/library/docker:cli); it drives the host daemon through
# DOCKER_SOCK (default /var/run/docker.sock), or the daemon at DOCKER_HOST when that is set (for
# example a docker:dind container, where the host runs podman: its API refuses plain http even on
# loopback, so the registry login fails there).
# Usage: OA_TOKEN=... [DOCKER_SOCK=... | DOCKER_HOST=tcp://127.0.0.1:2375] scripts/smoke/docker.sh [URL]
# shellcheck source=lib.sh
. "$(dirname "$0")/lib.sh"
smoke_init docker docker.io/library/docker:cli "$@"

daemon=(-v "${DOCKER_SOCK:-/var/run/docker.sock}:/var/run/docker.sock")
[ -z "${DOCKER_HOST:-}" ] || daemon=(-e DOCKER_HOST)

smoke_run "$(cat <<'SCRIPT'
set -eu
docker --version
hp="${OA_URL#*://}"
ref="$hp/$REPO:$VERSION"
docker pull -q docker.io/library/busybox:latest >/dev/null
docker run --name "$PKG" docker.io/library/busybox:latest sh -c "echo $PKG > /marker"
docker commit -c 'CMD ["cat","/marker"]' "$PKG" "$ref" >/dev/null
docker rm "$PKG" >/dev/null
echo "$OA_TOKEN" | docker login "$hp" -u smoke --password-stdin >/dev/null
docker push "$ref" >/dev/null
docker rmi "$ref" >/dev/null
docker pull -q "$ref" >/dev/null
[ "$(docker run --rm "$ref")" = "$PKG" ]
docker logout "$hp" >/dev/null
SCRIPT
)" "${daemon[@]}"
