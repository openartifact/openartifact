#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Start (and seed) a throwaway OpenArtifact server for the black-box suites:
# Schemathesis API fuzzing, Playwright e2e and the monkey test. Needs a
# reachable Postgres and a built server binary (the e2e suites need one built
# after `bun run build` so the SPA is embedded).
#
# Usage:
#   scripts/testenv.sh start   # start the server, seed it, write $OA_TESTENV_DIR/env
#   scripts/testenv.sh stop    # stop the server
#   . "$OA_TESTENV_DIR/env"    # OA_URL, OA_ADMIN_TOKEN, OA_ADMIN_USER/PASS, OA_DEV_USER/PASS
#
# Environment (defaults):
#   OA_BIN           server binary (bin/openartifact)
#   OA_TESTENV_DIR   state directory (${TMPDIR:-/tmp}/oa-testenv)
#   OA_PORT          listen port (18080)
#   DB_HOST/DB_PORT/DB_USER/DB_PASSWORD/DB_NAME   Postgres (127.0.0.1:5432 postgres/postgres/openartifact_test)
#   OA_EXTRA_ENV     extra "KEY=VALUE" words exported to the server (space separated)
#
# Seeded state: admin (password + API token "bootstrap"), user "dev" with
# read/write/delete on every repository (the developer role: no manage), and
# the repositories seed-npm (public), seed-npm-private, seed-raw (public),
# seed-pypi, seed-maven, seed-helm, seed-docker with one raw file in seed-raw.
set -euo pipefail

die() { echo "testenv: $*" >&2; exit 1; }

dir="${OA_TESTENV_DIR:-${TMPDIR:-/tmp}/oa-testenv}"
port="${OA_PORT:-18080}"
bin="${OA_BIN:-bin/openartifact}"
url="http://127.0.0.1:${port}"

api() { # api METHOD PATH [JSON] -> body on stdout, fails on non-2xx
	local method="$1" path="$2"
	shift 2
	local args=(-fsS -X "$method" -H "Authorization: Bearer ${OA_ADMIN_TOKEN}" "${url}${path}")
	if [ $# -gt 0 ]; then args+=(-H 'Content-Type: application/json' --data "$1"); fi
	curl "${args[@]}"
}

start() {
	command -v curl >/dev/null || die "curl is required"
	command -v jq >/dev/null || die "jq is required"
	[ -x "$bin" ] || die "server binary $bin not found (set OA_BIN)"
	rm -rf "$dir"
	mkdir -p "$dir/data"
	chmod 700 "$dir"
	local admin_pass="Admin-$(openssl rand -hex 12)"
	local dev_pass="Dev-$(openssl rand -hex 12)"
	local admin_token="oa_pat_$(openssl rand -hex 32)"
	printf '%s' "$admin_pass" >"$dir/admin-password"
	printf '%s' "$admin_token" >"$dir/admin-token"

	(
		export SERVER_HOST=127.0.0.1 SERVER_PORT="$port"
		export DB_HOST="${DB_HOST:-127.0.0.1}" DB_PORT="${DB_PORT:-5432}" DB_USER="${DB_USER:-postgres}"
		export DB_PASSWORD="${DB_PASSWORD:-postgres}" DB_NAME="${DB_NAME:-openartifact_test}" DB_SSLMODE=disable
		export STORAGE_BACKEND=filesystem STORAGE_PATH="$dir/data"
		export OPENARTIFACT_BOOTSTRAP_ADMIN_PASSWORD_FILE="$dir/admin-password"
		export OPENARTIFACT_BOOTSTRAP_ADMIN_TOKEN_FILE="$dir/admin-token"
		export OPENARTIFACT_RATE_LIMIT_RPS=0
		for kv in ${OA_EXTRA_ENV:-}; do export "${kv?}"; done
		setsid nohup "$bin" >"$dir/server.log" 2>&1 &
		echo $! >"$dir/server.pid"
	)

	local i
	for i in $(seq 1 60); do
		curl -fsS "$url/health" >/dev/null 2>&1 && break
		kill -0 "$(cat "$dir/server.pid")" 2>/dev/null || { cat "$dir/server.log" >&2; die "server exited during startup"; }
		sleep 1
		[ "$i" = 60 ] && { cat "$dir/server.log" >&2; die "server not healthy after 60s"; }
	done

	OA_ADMIN_TOKEN="$admin_token"
	seed "$dev_pass"

	cat >"$dir/env" <<EOF
export OA_URL='$url'
export OA_ADMIN_USER='admin'
export OA_ADMIN_PASS='$admin_pass'
export OA_ADMIN_TOKEN='$admin_token'
export OA_DEV_USER='dev'
export OA_DEV_PASS='$dev_pass'
EOF
	chmod 600 "$dir/env"
	echo "testenv: server up at $url, state in $dir/env" >&2
}

seed() {
	local dev_pass="$1" uid tid
	uid=$(api POST /api/v1/users "$(jq -nc --arg p "$dev_pass" '{username:"dev",email:"dev@example.com",password:$p}')" | jq -r .id)
	tid=$(api POST /api/v1/permission-targets '{"name":"developers","repository_filter":"*","path_pattern":"*","actions":["read","write","delete"]}' | jq -r .id)
	api POST "/api/v1/permission-targets/$tid/assignments" "$(jq -nc --arg u "$uid" '{principal_type:"user",principal_id:$u}')" >/dev/null

	local spec name format public
	for spec in seed-npm:npm:true seed-npm-private:npm:false seed-raw:raw:true seed-pypi:pypi:false \
		seed-maven:maven:false seed-helm:helm:false seed-docker:docker:false; do
		IFS=: read -r name format public <<<"$spec"
		api POST /api/v1/repositories "$(jq -nc --arg n "$name" --arg f "$format" --argjson p "$public" '{name:$n,format:$f,public:$p}')" >/dev/null
	done
	printf 'seed file\n' | curl -fsS -X PUT -H "Authorization: Bearer ${OA_ADMIN_TOKEN}" \
		--data-binary @- "${url}/repos/raw/seed-raw/hello.txt" >/dev/null
}

stop() {
	[ -f "$dir/server.pid" ] || return 0
	kill "$(cat "$dir/server.pid")" 2>/dev/null || true
	rm -f "$dir/server.pid"
}

case "${1:-}" in
start) start ;;
stop) stop ;;
*) sed -n '/^# Usage:/,/^set -euo/p' "$0" | sed '$d; s/^# \{0,1\}//'; exit 2 ;;
esac
