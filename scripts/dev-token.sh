#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Mint an access token for a running OpenArtifact instance and print only the
# token on stdout. Logs in with a session (POST /api/v1/auth/login), optionally
# completes the forced first-login password change, then calls
# POST /api/v1/auth/tokens with the session cookie and X-CSRF-Token (API tokens
# cannot mint tokens; see docs/integrations/api.md).
#
# Usage:
#   OA_PASS=... scripts/dev-token.sh [URL]
#   printf '%s' "$pw" | scripts/dev-token.sh --password-stdin [URL]
#
# Options:
#   --password-stdin   read the login password from stdin instead of OA_PASS
#   --change-password  when the account must change its password (the bootstrap
#                      admin), set it to $OA_NEW_PASS (min. 12 characters)
#                      and continue; fails if OA_NEW_PASS is unset
#   -h, --help         show this help
#
# Environment:
#   OA_URL         base URL (default http://127.0.0.1:8080; argument wins)
#   OA_USER        login user (default admin)
#   OA_PASS        login password (or --password-stdin)
#   OA_NEW_PASS    new password for --change-password
#   OA_TOKEN_NAME  token name (default dev-token)
#   OA_TOKEN_KIND  access (default) or deploy
#
# Secrets are never put on a command line: they are read from the environment
# or stdin and handed to curl through stdin / a private temp file.
set -euo pipefail

usage() { sed -n '/^# Usage:/,/^# Secrets/p' "$0" | sed 's/^# \{0,1\}//'; }
die() { echo "dev-token: $*" >&2; exit 1; }

stdin_pass=0
change_pass=0
url="${OA_URL:-http://127.0.0.1:8080}"
while [ $# -gt 0 ]; do
	case "$1" in
	--password-stdin) stdin_pass=1 ;;
	--change-password) change_pass=1 ;;
	-h | --help) usage; exit 0 ;;
	-*) die "unknown option: $1" ;;
	*) url="$1" ;;
	esac
	shift
done
url="${url%/}"

command -v curl >/dev/null || die "curl is required"
command -v jq >/dev/null || die "jq is required"

user="${OA_USER:-admin}"
if [ "$stdin_pass" = 1 ]; then
	IFS= read -r OA_PASS || [ -n "${OA_PASS:-}" ] || die "no password on stdin"
fi
[ -n "${OA_PASS:-}" ] || die "set OA_PASS or use --password-stdin"
export OA_PASS

umask 077
work="$(mktemp -d "${TMPDIR:-/tmp}/dev-token.XXXXXX")"
trap 'rm -rf "$work"' EXIT
jar="$work/jar"
body="$work/body"

# api METHOD PATH JSON-FROM-STDIN [extra curl args...]: writes the response body
# to $body and prints the HTTP status.
api() {
	local method="$1" path="$2"
	shift 2
	curl -sS -o "$body" -w '%{http_code}' -X "$method" "$url$path" \
		-b "$jar" -c "$jar" -H 'Content-Type: application/json' --data-binary @- "$@"
}

errmsg() { jq -r '.code // .error // .message // "unexpected response"' "$body" 2>/dev/null || echo "unexpected response"; }

status="$(jq -n --arg u "$user" --arg p "$OA_PASS" '{username:$u,password:$p}' | api POST /api/v1/auth/login)"
[ "$status" = 200 ] || die "login failed (HTTP $status: $(errmsg))"

csrf() { awk '$6=="oa_csrf"{print $7}' "$jar"; }

mint() {
	jq -n --arg n "${OA_TOKEN_NAME:-dev-token}" --arg k "${OA_TOKEN_KIND:-access}" \
		'{name:$n,kind:$k}' | api POST /api/v1/auth/tokens -H "X-CSRF-Token: $(csrf)"
}

status="$(mint)"
if [ "$status" = 403 ] && [ "$(jq -r '.error // .code // empty' "$body")" = password_change_required ]; then
	[ "$change_pass" = 1 ] || die "password change required: rerun with --change-password and OA_NEW_PASS"
	[ -n "${OA_NEW_PASS:-}" ] || die "--change-password needs OA_NEW_PASS"
	export OA_NEW_PASS
	status="$(jq -n --arg c "$OA_PASS" --arg n "$OA_NEW_PASS" '{current_password:$c,new_password:$n}' |
		api POST /api/v1/auth/change-password -H "X-CSRF-Token: $(csrf)")"
	[ "$status" = 204 ] || die "password change failed (HTTP $status: $(errmsg))"
	status="$(mint)"
fi
[ "$status" = 200 ] || [ "$status" = 201 ] || die "token creation failed (HTTP $status: $(errmsg))"

jq -er '.token' "$body" || die "response has no token"
