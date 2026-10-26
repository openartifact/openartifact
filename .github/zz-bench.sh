#!/bin/bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
# Temporary benchmark helper, removed before merge.
# usage: zz-bench.sh <tag> <build.sh override or ""> <jobs>
set -eu
tag=$1; script=$2; jobs=$3
out="$RUNNER_TEMP/out-$tag"
mkdir -p "$out"
mount=()
[ -n "$script" ] && mount=(-v "$script:/src/build.sh:ro")
vmstat 5 > "$RUNNER_TEMP/vm-$tag.log" &
vm=$!
start=$(date +%s)
docker run --rm -e FUZZING_ENGINE=libfuzzer -e SANITIZER=address -e ARCHITECTURE=x86_64 \
  -e FUZZING_LANGUAGE=go -e PROJECT_NAME=openartifact -e FUZZ_BUILD_JOBS="$jobs" \
  "${mount[@]}" -v "$out:/out" oa-cfl compile > "$RUNNER_TEMP/log-$tag.txt" 2>&1 || echo "compile failed"
end=$(date +%s)
kill "$vm" || true
echo "BENCH $tag jobs=$jobs compile_seconds=$((end - start))"
ls "$out" | grep -vcE '\.(dict|options|zip|json|gocovpath)$' || true
awk 'NR>2 {us+=$13; sy+=$14; id+=$15; wa+=$16; st+=$17; n++} END {printf "BENCH %s vmstat avg us=%.0f sy=%.0f id=%.0f wa=%.0f st=%.0f samples=%d\n", "'"$tag"'", us/n, sy/n, id/n, wa/n, st/n, n}' "$RUNNER_TEMP/vm-$tag.log"
tail -3 "$RUNNER_TEMP/log-$tag.txt"
rm -rf "$out"
