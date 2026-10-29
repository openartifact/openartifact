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
# package name), VERSION. Containers use the host network, so OA_URL may be
# http://127.0.0.1:PORT; the token never appears on a command line.
set -euo pipefail

die() { echo "smoke: $*" >&2; exit 1; }

# smoke_init FORMAT IMAGE [URL]: checks the prerequisites and creates the hosted
# repository smoke-FORMAT-ID. The URL wins over OA_URL (default
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
	SMOKE_ID="$(date +%s)$RANDOM"
	SMOKE_REPO="smoke-$1-$SMOKE_ID"
	SMOKE_CREATED=()
	export OA_URL OA_TOKEN
	trap smoke_cleanup EXIT
	smoke_create_repo "$SMOKE_REPO" "$1"
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

smoke_cleanup() {
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
	docker run --rm --network host -e OA_URL -e OA_TOKEN -e REPO="$SMOKE_REPO" \
		-e PKG="smoke${SMOKE_ID}" -e VERSION=1.0.0 "$@" --entrypoint "${SMOKE_SHELL:-sh}" "$SMOKE_IMAGE" -c "$script"
}
