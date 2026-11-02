#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# docker: docker login, tag, push, remove, pull and run an image through /v2.
# Image: DOCKER_IMAGE (default docker.io/library/docker:cli); it drives the host daemon through
# DOCKER_SOCK, or the daemon at a tcp or ssh DOCKER_HOST when that is set (for example a docker:dind
# container; a unix DOCKER_HOST is ignored, use DOCKER_SOCK).
# Without either, the socket is the first of /var/run/docker.sock, podman's user socket
# ($XDG_RUNTIME_DIR/podman/podman.sock) and podman's system socket (/run/podman/podman.sock) that exists;
# when none does and podman is installed, a podman API service is started for the run and stopped
# after it. Podman's API refuses a plain-http registry, so on a podman host with an http:// OA_URL the
# script always starts such a service, with a registries.conf that marks the server's host:port insecure.
# Usage: OA_TOKEN=... [DOCKER_SOCK=... | DOCKER_HOST=tcp://127.0.0.1:2375] scripts/smoke/docker.sh [URL]
# shellcheck source=lib.sh
. "$(dirname "$0")/lib.sh"
smoke_init docker docker.io/library/docker:cli "$@"

# smoke_podman_service: starts a podman API service of its own on a private socket and sets SMOKE_SOCK to it
# (not run in a subshell: this shell's trap stops the service). Podman's API refuses a plain-http registry,
# even on loopback, unless its registries.conf lists it as insecure: the private service gets one that does.
smoke_podman_service() {
	local dir i=0 pid
	dir="$(mktemp -d)"
	printf '[[registry]]\nlocation = "%s"\ninsecure = true\n' "${OA_URL#*://}" >"$dir/registries.conf"
	CONTAINERS_REGISTRIES_CONF="$dir/registries.conf" podman system service --time=0 "unix://$dir/podman.sock" >/dev/null 2>&1 &
	pid=$!
	# shellcheck disable=SC2064 # the values are fixed now
	trap "kill $pid 2>/dev/null || true; rm -rf '$dir'; smoke_cleanup" EXIT
	until [ -S "$dir/podman.sock" ]; do
		i=$((i + 1))
		[ "$i" -lt 50 ] || die "podman system service did not start"
		sleep 0.2
	done
	SMOKE_SOCK="$dir/podman.sock"
}

# smoke_docker_socket: sets SMOKE_SOCK to the socket of the container engine the client container drives:
# DOCKER_SOCK; else, for a podman host and a plain-http OA_URL, a private podman service that trusts that
# registry; else the first of /var/run/docker.sock and podman's user and system sockets that exists; else a
# private podman service.
smoke_docker_socket() {
	local c
	if [ -n "${DOCKER_SOCK:-}" ]; then
		SMOKE_SOCK="$DOCKER_SOCK"
		return 0
	fi
	case "$OA_URL" in
	http://*) if docker --version 2>/dev/null | grep -qi podman; then
		smoke_podman_service
		return 0
	fi ;;
	esac
	for c in /var/run/docker.sock "${XDG_RUNTIME_DIR:-/run/user/$(id -u)}/podman/podman.sock" /run/podman/podman.sock; do
		if [ -S "$c" ]; then
			SMOKE_SOCK="$c"
			return 0
		fi
	done
	command -v podman >/dev/null || die "no container engine socket: set DOCKER_SOCK or DOCKER_HOST"
	smoke_podman_service
}

# A unix DOCKER_HOST names a path of the host, which the client container does not have (the shell of a
# podman user often exports one for testcontainers): it is not a choice of daemon, DOCKER_SOCK is.
case "${DOCKER_HOST:-}" in unix://*) unset DOCKER_HOST ;; esac

if [ -n "${DOCKER_HOST:-}" ]; then
	daemon=(-e DOCKER_HOST)
else
	smoke_docker_socket
	# label=disable: under SELinux a container may not use a host socket otherwise (podman, rootless or not).
	daemon=(-v "$SMOKE_SOCK:/var/run/docker.sock" --security-opt label=disable)
fi

smoke_run "$(cat <<'SCRIPT'
set -eu
docker --version
hp="${OA_URL#*://}"
ref="$hp/$REPO:$VERSION"
# Podman's API cannot log in to a plain-http registry (its /auth ignores insecure registries); the
# client then sends the credentials from its config file, which needs no login round trip.
if ! echo "$OA_TOKEN" | docker login "$hp" -u smoke --password-stdin >/dev/null 2>&1; then
  mkdir -p "$HOME/.docker"
  printf '{"auths":{"%s":{"auth":"%s"}}}' "$hp" "$(printf 'smoke:%s' "$OA_TOKEN" | base64 | tr -d '\n')" > "$HOME/.docker/config.json"
fi
[ "$SMOKE_MODE" = install ] || {
  docker pull -q docker.io/library/busybox:latest >/dev/null
  docker run --name "$PKG" docker.io/library/busybox:latest sh -c "echo $PKG > /marker"
  docker commit -c 'CMD ["cat","/marker"]' "$PKG" "$ref" >/dev/null
  docker rm "$PKG" >/dev/null
  docker push "$ref" >/dev/null
  docker rmi "$ref" >/dev/null
}
[ "$SMOKE_MODE" = publish ] || {
  # the daemon is shared: make sure it holds no copy of the image, so the pull comes from the registry
  docker rmi "$ref" >/dev/null 2>&1 || true
  docker pull -q "$ref" >/dev/null
  [ "$(docker run --rm "$ref")" = "$PKG" ]
}
docker logout "$hp" >/dev/null
SCRIPT
)" "${daemon[@]}"
