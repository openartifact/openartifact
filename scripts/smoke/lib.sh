# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# shellcheck shell=bash
#
# Shared helpers of the per-format client smoke tests (scripts/smoke/<format>.sh).
# Sourced, not executed. Every format script follows one shape:
#
#   . "$(dirname "$0")/lib.sh"
#   smoke_init <format> <default image> "$@" # optional URL argument, one repository
#   smoke_run  <container script>            # run the real client in a container
#
# The container script is POSIX sh (or the shell named by SMOKE_SHELL, set
# before smoke_run; the image entrypoint is replaced by it) and finds
# its parameters in the environment: OA_URL, OA_TOKEN, REPO, PKG (a unique
# package name), VERSION, SMOKE_MODE. Containers use the host network, so
# OA_URL may be http://127.0.0.1:PORT; the token never appears on a command line.
#
# Two-phase runs (upgrade proofs): SMOKE_MODE selects what a run does.
#   both     (default) create the repository, publish, install, delete the repository
#   publish  create the repository, publish, keep everything (implies KEEP=1)
#   install  create nothing, install what a publish run with the same SMOKE_ID
#            published (the repository may have been imported meanwhile), delete nothing
# The container script guards its two parts with the mode:
#   [ "$SMOKE_MODE" = install ] || { publish; }
#   [ "$SMOKE_MODE" = publish ] || { install; }
# SMOKE_ID (default: time and a random number) fixes the names: repository
# smoke-<format>-<id> (SMOKE_REPO_NAME overrides it), package smoke<id>, version 1.0.0.
set -euo pipefail

die() { echo "smoke: $*" >&2; exit 1; }

# smoke_init FORMAT IMAGE [URL]: checks the prerequisites and, unless
# SMOKE_MODE=install, creates the hosted repository smoke-FORMAT-ID (SMOKE_REPO_BODY,
# a JSON object, is merged over the creation request). The URL wins over OA_URL (default
# http://127.0.0.1:8080); the image is overridable with SMOKE_IMAGE or the
# per-format <FORMAT>_IMAGE variable.
smoke_init() {
	SMOKE_FORMAT="$1"
	local var
	var="$(printf '%s' "$1" | tr '[:lower:]-' '[:upper:]_')_IMAGE"
	SMOKE_IMAGE="${SMOKE_IMAGE:-${!var:-$2}}"
	# Docker Hub short names, fully qualified so podman (no short-name search) resolves them too.
	case "$SMOKE_IMAGE" in
	*/*) case "${SMOKE_IMAGE%%/*}" in *.* | *:* | localhost) ;; *) SMOKE_IMAGE="docker.io/$SMOKE_IMAGE" ;; esac ;;
	*) SMOKE_IMAGE="docker.io/library/$SMOKE_IMAGE" ;;
	esac
	OA_URL="${3:-${OA_URL:-http://127.0.0.1:8080}}"
	OA_URL="${OA_URL%/}"
	[ -n "${OA_TOKEN:-}" ] || die "set OA_TOKEN (see scripts/dev-token.sh)"
	for c in docker curl jq; do command -v "$c" >/dev/null || die "$c is required"; done
	SMOKE_MODE="${SMOKE_MODE:-both}"
	case "$SMOKE_MODE" in both | publish | install) ;; *) die "SMOKE_MODE must be both, publish or install (got $SMOKE_MODE)" ;; esac
	[ "$SMOKE_MODE" != publish ] || KEEP=1
	SMOKE_ID="${SMOKE_ID:-$(date +%s)$RANDOM}"
	SMOKE_REPO="${SMOKE_REPO_NAME:-smoke-$1-$SMOKE_ID}"
	SMOKE_PKG="smoke${SMOKE_ID}"
	SMOKE_VERSION=1.0.0
	SMOKE_CREATED=()
	export OA_URL OA_TOKEN SMOKE_MODE
	echo "smoke: repo=$SMOKE_REPO pkg=$SMOKE_PKG version=$SMOKE_VERSION mode=$SMOKE_MODE" >&2
	trap smoke_cleanup EXIT
	if [ "$SMOKE_MODE" = install ]; then
		smoke_require_repo "$SMOKE_REPO"
	else
		smoke_create_repo "$SMOKE_REPO" "$1" "${SMOKE_REPO_BODY:-}"
	fi
}

# smoke_require_repo NAME: the repository of an install run must exist already.
smoke_require_repo() {
	local code
	code="$(smoke_api GET "/api/v1/repository/$1" -o /dev/null -w '%{http_code}')"
	[ "$code" = 200 ] || die "repository $1 does not exist (HTTP $code): run the publish phase (SMOKE_MODE=publish, same SMOKE_ID) first"
}

# smoke_api METHOD PATH [curl args...]: an authenticated call to the REST API.
smoke_api() {
	local method="$1" path="$2"
	shift 2
	curl -sS -X "$method" "$OA_URL$path" -H "Authorization: Bearer $OA_TOKEN" "$@"
}

# smoke_create_repo NAME FORMAT [JSON-OBJECT]: creates a private hosted
# repository; the optional object is merged over the request body.
smoke_create_repo() {
	local code extra="${3:-}"
	[ -n "$extra" ] || extra='{}'
	code="$(jq -n --arg n "$1" --arg f "$2" --argjson x "$extra" '{name:$n,format:$f,public:false} + $x' |
		smoke_api POST /api/v1/repositories -H 'Content-Type: application/json' --data-binary @- \
			-o /dev/null -w '%{http_code}')"
	[ "$code" = 200 ] || [ "$code" = 201 ] || die "creating repository $1 failed (HTTP $code)"
	SMOKE_CREATED+=("$1")
}

# smoke_cleanup: deletes what this run created; only a run of mode both does.
smoke_cleanup() {
	[ "$SMOKE_MODE" = both ] || return 0
	[ "${KEEP:-0}" = 1 ] && return 0
	local r
	for r in "${SMOKE_CREATED[@]}"; do
		smoke_api DELETE "/api/v1/repository/$r?confirm=$r" -o /dev/null || true
	done
}

# smoke_run SCRIPT [docker run args...]: runs SCRIPT in a throwaway container of
# $SMOKE_IMAGE; its exit status is the format's verdict.
smoke_run() {
	local script="$1"
	shift
	echo "==> $SMOKE_FORMAT: $SMOKE_IMAGE"
	docker run --rm --network host -e OA_URL -e OA_TOKEN -e SMOKE_MODE -e REPO="$SMOKE_REPO" \
		-e PKG="$SMOKE_PKG" -e VERSION="$SMOKE_VERSION" "$@" --entrypoint "${SMOKE_SHELL:-sh}" "$SMOKE_IMAGE" -c "$script"
}
