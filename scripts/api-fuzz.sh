#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Fuzz the REST API with Schemathesis against internal/api/openapi.json.
# Needs a server started by scripts/testenv.sh (source its env file first so
# OA_URL / OA_ADMIN_TOKEN / OA_DEV_USER / OA_DEV_PASS are set).
#
# Usage:
#   scripts/api-fuzz.sh [pr|nightly]     # default: pr
#
# Three passes, each failing the script when Schemathesis reports a failure:
#   admin      Bearer admin token. not_a_server_error, response_schema_conformance,
#              status_code_conformance and ignored_auth (the built-in check that
#              replays each successful request without/with invalid credentials
#              and fails when the operation still answers 2xx).
#   developer  Bearer token of the seeded `dev` user (developer role, no manage).
#              Same checks plus admin_forbidden_for_developer: no /api/v1/admin/*
#              operation may answer 2xx.
#   anonymous  No credentials at all. Only anonymous_rejected (tests/apifuzz/hooks.py:
#              a 2xx is allowed only for operations the spec marks `security: []`
#              and for reads of public repositories) and not_a_server_error.
#
# Operations that must not be fuzzed are disabled in tests/apifuzz/schemathesis.toml
# (each with its reason); known issues deferred from the first triage are in the
# same file under "Known issues".
#
# Budgets: pr = small, fixed seed (deterministic, ~5 min); nightly = bigger, random
# seed printed (reproduce with OA_FUZZ_SEED=<seed>).
#
# Environment:
#   OA_FUZZ_SEED           override the seed
#   OA_FUZZ_EXAMPLES       override --max-examples per operation
#   OA_FUZZ_PASSES        space separated subset of: admin developer anonymous
#   OA_FUZZ_REPORT_DIR     reports/cassettes (default: ./schemathesis-report)
#   SCHEMATHESIS           command line of the Schemathesis CLI. Default: the pinned
#                          container image (host network). For a quick local run:
#                          SCHEMATHESIS='uvx schemathesis@4.32.5'
set -euo pipefail

mode="${1:-pr}"
root="$(cd "$(dirname "$0")/.." && pwd)"
report_dir="$(realpath -m "${OA_FUZZ_REPORT_DIR:-schemathesis-report}")"

# Keep the tag and digest in sync: the digest is the multi-arch index of the tag.
IMAGE='docker.io/schemathesis/schemathesis:4.32.5@sha256:e414b3466a95d03fc6d9de1b7842e1d4738775493750166035b943c0bdcce248'

: "${OA_URL:?source the testenv env file first}"
: "${OA_ADMIN_TOKEN:?source the testenv env file first}"
: "${OA_DEV_USER:?source the testenv env file first}"
: "${OA_DEV_PASS:?source the testenv env file first}"

case "$mode" in
pr)
	examples=40
	seed="${OA_FUZZ_SEED:-20261026}"
	;;
nightly)
	examples=100
	seed="${OA_FUZZ_SEED:-$(od -An -N4 -tu4 /dev/urandom | tr -d ' ')}"
	;;
*)
	echo "usage: $0 [pr|nightly]" >&2
	exit 2
	;;
esac
examples="${OA_FUZZ_EXAMPLES:-$examples}"
echo "api-fuzz: mode=$mode seed=$seed max-examples=$examples url=$OA_URL"

mkdir -p "$report_dir"

# A session-less API token for the developer: log in, then mint a token with the
# CSRF double-submit header.
dev_token() {
	local jar csrf
	jar="$(mktemp)"
	curl -fsS -c "$jar" -o /dev/null -X POST "$OA_URL/api/v1/auth/login" \
		-H 'Content-Type: application/json' \
		--data "$(jq -nc --arg u "$OA_DEV_USER" --arg p "$OA_DEV_PASS" '{username:$u,password:$p}')"
	csrf="$(awk '$6=="oa_csrf"{print $7}' "$jar")"
	curl -fsS -b "$jar" -X POST "$OA_URL/api/v1/auth/tokens" \
		-H 'Content-Type: application/json' -H "X-CSRF-Token: $csrf" \
		--data '{"name":"api-fuzz","kind":"access"}' | jq -r .token
	rm -f "$jar"
}

# Comma separated ids of the users, tokens and permission targets that exist now:
# the hooks keep the fuzzer from deleting what the run authenticates with.
protected_ids() {
	local ep
	for ep in users permission-targets admin/tokens; do
		curl -fsS -H "Authorization: Bearer $OA_ADMIN_TOKEN" "$OA_URL/api/v1/$ep?limit=100" | jq -r '.data[].id'
	done | paste -sd,
}

# schemathesis ARGS...: run the CLI, in the pinned container unless SCHEMATHESIS
# points at a local one. $spec/$fuzz/$out are the spec, config dir and report dir
# as that CLI sees them.
schemathesis() {
	if [ -n "${SCHEMATHESIS:-}" ]; then
		# The CLI writes a .schemathesis cache next to its cwd: keep it out of the tree.
		# shellcheck disable=SC2086
		(cd "$report_dir" && SCHEMATHESIS_HOOKS="$fuzz/hooks.py" $SCHEMATHESIS "$@")
	else
		# Run as the caller so the mounted report dir is writable (the image user is not).
		docker run --rm --network host --user "$(id -u):$(id -g)" -w /report \
			-e HOME=/report -e PYTHONDONTWRITEBYTECODE=1 \
			-e SCHEMATHESIS_HOOKS=/fuzz/hooks.py -e OA_FUZZ_PRINCIPAL -e OA_FUZZ_PUBLIC_REPOS -e OA_FUZZ_PROTECTED_IDS \
			-v "$root/internal/api/openapi.json:/spec/openapi.json:ro" \
			-v "$root/tests/apifuzz:/fuzz:ro" \
			-v "$report_dir:/report" "$IMAGE" "$@"
	fi
}

if [ -n "${SCHEMATHESIS:-}" ]; then
	spec="$root/internal/api/openapi.json" fuzz="$root/tests/apifuzz" out="$report_dir"
else
	spec=/spec/openapi.json fuzz=/fuzz out=/report
fi

status=0

# run NAME CHECKS [extra schemathesis args...]; keeps going after a failing pass
# so one run reports every problem.
run() {
	local name="$1" checks="$2"
	shift 2
	echo "api-fuzz: === pass $name ==="
	OA_FUZZ_PROTECTED_IDS="$(protected_ids)"
	export OA_FUZZ_PROTECTED_IDS
	schemathesis --config-file "$fuzz/schemathesis.toml" run "$spec" --url "$OA_URL" \
		--checks "$checks" --max-examples "$examples" --seed "$seed" \
		--phases examples,coverage,fuzzing --workers 1 --request-timeout 20 \
		--report junit,vcr --report-dir "$out/$name" --no-color "$@" || status=1
}

builtin=not_a_server_error,response_schema_conformance,status_code_conformance,ignored_auth
passes=" ${OA_FUZZ_PASSES:-admin developer anonymous} "

case "$passes" in *" admin "*)
	run admin "$builtin" -H "Authorization: Bearer $OA_ADMIN_TOKEN"
	;;
esac

case "$passes" in *" developer "*)
	dev_tok="$(dev_token)"
	OA_FUZZ_PRINCIPAL=developer run developer "$builtin,admin_forbidden_for_developer" \
		-H "Authorization: Bearer $dev_tok"
	;;
esac

case "$passes" in *" anonymous "*)
	run anonymous not_a_server_error,anonymous_rejected --generation-with-security-parameters false
	;;
esac

exit "$status"
