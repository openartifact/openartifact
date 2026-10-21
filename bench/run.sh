#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Seeds a running OpenArtifact instance and runs the HTTP load scenarios.
#
#   OA_URL=http://127.0.0.1:8080 OA_USER=admin OA_PASS=... OA_PID=<server pid> bench/run.sh
#
# Optional: OA_DURATION (default 8s), OA_CONCURRENCY (32), OA_OUT (bench-out),
# OA_ONLY (comma-separated scenario substrings), PSQL (a psql command line that
# reaches the server's database; enables pg_stat_statements capture, which needs
# the extension loaded: shared_preload_libraries=pg_stat_statements).
set -euo pipefail

: "${OA_URL:?set OA_URL}" "${OA_USER:?set OA_USER}" "${OA_PASS:?set OA_PASS}"
out="${OA_OUT:-bench-out}"
mkdir -p "$out"

go build -o "$out/oabench" ./bench/oabench

if [ ! -f "$out/state.json" ]; then
	"$out/oabench" seed -url "$OA_URL" -user "$OA_USER" -pass "$OA_PASS" -state "$out/state.json"
fi

[ -n "${PSQL:-}" ] && $PSQL -Atc "select pg_stat_statements_reset()" >/dev/null

args=(-state "$out/state.json" -duration "${OA_DURATION:-8s}" -concurrency "${OA_CONCURRENCY:-32}"
	-metrics "$OA_URL/metrics" -json "$out/result.json")
[ -n "${OA_PID:-}" ] && args+=(-pid "$OA_PID")
[ -n "${OA_ONLY:-}" ] && args+=(-only "$OA_ONLY")

"$out/oabench" load "${args[@]}" | tee "$out/result.txt"

if [ -n "${PSQL:-}" ]; then
	$PSQL -c "select calls, round(total_exec_time::numeric) total_ms, round(mean_exec_time::numeric,2) mean_ms, rows, left(regexp_replace(query, '\s+', ' ', 'g'), 110) query from pg_stat_statements order by total_exec_time desc limit 15" | tee "$out/pg_stat_statements.txt"
fi
