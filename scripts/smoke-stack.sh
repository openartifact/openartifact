#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Start, inspect and remove a throwaway OpenArtifact compose stack (an isolated
# `docker compose -p` project) for the client smoke tests. `up` generates its own
# secrets, waits until the server is healthy, mints an administrator token and
# prints only `export OA_URL=...` / `export OA_TOKEN=...` on stdout, so
#   eval "$(scripts/smoke-stack.sh up)" && scripts/smoke-formats.sh
# runs against the fresh stack.
#
# Usage:
#   scripts/smoke-stack.sh up|down|logs
#
# Environment:
#   OA_STACK_PROJECT  compose project name (default oa-smoke)
#   OA_STACK_PORT     published host port (default 18080)
#   OA_STACK_BUILD    1 builds the image on `up` (default); 0 uses the image
#                     already tagged <project>-openartifact (CI loads it)
#   OPENARTIFACT_RUNTIME  runtime base image (default distroless: no dhi.io login)
#
# Secrets live in $OA_STACK_DIR (default $TMPDIR/<project>, mode 700) until `down`.
set -euo pipefail

die() { echo "smoke-stack: $*" >&2; exit 1; }
log() { echo "smoke-stack: $*" >&2; }

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
project="${OA_STACK_PROJECT:-oa-smoke}"
port="${OA_STACK_PORT:-18080}"
dir="${OA_STACK_DIR:-${TMPDIR:-/tmp}/$project}"
envfile="$dir/env"
export OPENARTIFACT_RUNTIME="${OPENARTIFACT_RUNTIME:-distroless}"

compose() {
	docker compose -p "$project" -f "$root/docker/docker-compose.yml" --env-file "$envfile" "$@"
}

hex() { head -c "$1" /dev/urandom | od -An -tx1 | tr -d ' \n'; }

cmd="${1:-}"
case "$cmd" in
up)
	command -v docker >/dev/null || die "docker is required"
	[ ! -e "$envfile" ] || die "stack $project already exists (run: $0 down)"
	(umask 077 && mkdir -p "$dir")
	pass="$(hex 12)"
	(umask 077 && cat >"$envfile" <<ENV
POSTGRES_PASSWORD=$(hex 16)
OPENARTIFACT_SECRET_KEY=$(hex 32)
AUTH_HMAC_KEY=$(hex 32)
OPENARTIFACT_BOOTSTRAP_ADMIN_PASSWORD=$pass
OPENARTIFACT_PORT=$port
BASE_URL=http://127.0.0.1:$port
ENV
	)
	url="http://127.0.0.1:$port"
	# A stack that a failed `up` left behind keeps a database volume sealed with other secrets.
	compose down -v --remove-orphans >&2 || true
	build=--no-build
	[ "${OA_STACK_BUILD:-1}" = 1 ] && build=--build
	if ! compose up -d "$build" --wait --wait-timeout 300 >&2; then
		compose logs --no-color >&2 || true
		die "the stack did not become healthy (it is left running: $0 down removes it)"
	fi
	if ! token="$(OA_PASS="$pass" OA_NEW_PASS="$(hex 12)" "$root/scripts/dev-token.sh" --change-password "$url")"; then
		compose logs openartifact >&2 || true
		die "could not mint a token"
	fi
	printf 'export OA_URL=%q\nexport OA_TOKEN=%q\n' "$url" "$token"
	;;
down)
	[ -e "$envfile" ] || die "no stack $project in $dir"
	compose down -v --remove-orphans >&2
	rm -rf "$dir"
	;;
logs)
	[ -e "$envfile" ] || die "no stack $project in $dir"
	compose logs --no-color
	;;
*)
	die "usage: $0 up|down|logs"
	;;
esac
