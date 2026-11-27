#!/bin/bash -eu
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later

# Test of fuzz-issue.sh with a stub `gh` that serves fixtures and records the
# issue writes: what a crash, an infrastructure failure, a timeout, a cancel
# and a passing run do to the tracking issue. The fixtures have the shape the
# GitHub API returned for the nightly runs of 2026-11-27 (job timed out) and
# 2026-11-20 (storage repository answered HTTP 403, no crash). Needs jq.
# Run from anywhere: scripts/fuzz-issue_test.sh (CI job `go`).

script=$(cd "$(dirname "$0")" && pwd)/fuzz-issue.sh
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

mkdir "$tmp/bin" "$tmp/fix"
fix=$tmp/fix

# The stub answers the calls the script makes: the failed log, the jobs of the
# run, a job's annotations and the issue list come from $FIX; issue writes are
# appended to $FIX/calls. --jq is applied as gh does (strings raw, else JSON).
cat > "$tmp/bin/gh" << 'STUB'
#!/bin/bash -eu
expr=.
args=("$@")
for ((i = 0; i < ${#args[@]}; i++)); do
  if [ "${args[i]}" = --jq ]; then expr=${args[i + 1]}; fi
done
serve() { jq -r "($expr) | if type == \"string\" then . else tojson end" "$1"; }
case "$1 $2" in
"run view") cat "$FIX/log" ;;
"api repos/"*"/actions/runs/"*"/jobs"*) serve "$FIX/jobs.json" ;;
"api repos/"*"/check-runs/"*"/annotations") serve "$FIX/annotations-$(basename "$(dirname "$2")").json" ;;
"issue list") serve "$FIX/issues.json" ;;
"issue create" | "issue comment" | "issue close") printf 'gh %s\n' "$*" >> "$FIX/calls" ;;
*)
  echo "stub gh: unexpected call: $*" >&2
  exit 1
  ;;
esac
STUB
chmod +x "$tmp/bin/gh"

fail=0
check() { # <name> <want> <got>
  if [ "$2" = "$3" ]; then
    echo "ok   $1"
  else
    echo "FAIL $1"
    echo "  want: $2"
    echo "  got:  $3"
    fail=1
  fi
}

# has <name> <text> <calls>: the calls hold the text.
has() {
  if [[ $3 == *"$2"* ]]; then
    echo "ok   $1"
  else
    echo "FAIL $1"
    echo "  want: $2"
    echo "  in:   $3"
    fail=1
  fi
}

# hasnt <name> <text> <calls>: the calls do not hold the text.
hasnt() {
  if [[ $3 == *"$2"* ]]; then
    echo "FAIL $1"
    echo "  found: $2"
    echo "  in:    $3"
    fail=1
  else
    echo "ok   $1"
  fi
}

# run <result>: the script on the fixtures, the recorded issue writes on stdout.
run() {
  rm -f "$fix/calls"
  PATH=$tmp/bin:$PATH FIX=$fix GITHUB_REPOSITORY=openartifact/openartifact GITHUB_RUN_ID=77 \
    bash "$script" "ClusterFuzzLite batch" "$1" > "$fix/out" 2>&1 || {
    cat "$fix/out"
    return 1
  }
  if [ -e "$fix/calls" ]; then cat "$fix/calls"; fi
}

no_issue='[]'
open_issue='[{"number":42,"title":"Nightly fuzz failure: ClusterFuzzLite batch"},{"number":7,"title":"Nightly fuzz failure: API fuzz"}]'

# 2026-11-27: the 120-minute timeout cancelled the job during "Run fuzzers".
cat > "$fix/jobs.json" << 'JSON'
{"total_count":2,"jobs":[
 {"id":140474670992,"name":"batch fuzzing","conclusion":"cancelled","steps":[
  {"name":"Check the storage token","conclusion":"success","started_at":"2026-11-27T11:22:27Z","completed_at":"2026-11-27T11:22:28Z"},
  {"name":"Build fuzzers","conclusion":"success","started_at":"2026-11-27T11:22:28Z","completed_at":"2026-11-27T12:04:34Z"},
  {"name":"Run fuzzers","conclusion":"cancelled","started_at":"2026-11-27T12:04:34Z","completed_at":"2026-11-27T13:22:10Z"},
  {"name":"Upload SARIF","conclusion":"skipped","started_at":"2026-11-27T13:22:10Z","completed_at":"2026-11-27T13:22:10Z"}]},
 {"id":140490000001,"name":"corpus pruning","conclusion":"success","steps":[]}]}
JSON
cat > "$fix/annotations-140474670992.json" << 'JSON'
[{"annotation_level":"failure","message":"The job has exceeded the maximum execution time of 2h0m0s"},
 {"annotation_level":"failure","message":"The operation was canceled."}]
JSON
: > "$fix/log" # a cancelled step has no failed log
echo "$no_issue" > "$fix/issues.json"

calls=$(run cancelled)
has "timeout: opens the issue" "gh issue create" "$calls"
has "timeout: names the job" 'job "batch fuzzing" cancelled (timed out)' "$calls"
has "timeout: names the step and how long it ran" 'step "Run fuzzers" cancelled after 77 min' "$calls"
hasnt "timeout: not the steps that went well" 'step "Build fuzzers"' "$calls"
hasnt "timeout: not the jobs that went well" 'corpus pruning' "$calls"

echo "$open_issue" > "$fix/issues.json"
calls=$(run cancelled)
has "timeout: comments on the open issue" "gh issue comment 42" "$calls"

# A cancel by hand has the second annotation only.
cat > "$fix/annotations-140474670992.json" << 'JSON'
[{"annotation_level":"failure","message":"The operation was canceled."}]
JSON
check "cancelled by hand: changes nothing" "" "$(run cancelled)"
echo "$no_issue" > "$fix/issues.json"
check "cancelled by hand, no issue: changes nothing" "" "$(run cancelled)"

# 2026-11-20: the storage repository answered HTTP 403, there was no crash.
cat > "$fix/jobs.json" << 'JSON'
{"total_count":1,"jobs":[
 {"id":136229890096,"name":"batch fuzzing","conclusion":"failure","steps":[
  {"name":"Build fuzzers","conclusion":"success","started_at":"2026-11-20T10:25:50Z","completed_at":"2026-11-20T11:00:59Z"},
  {"name":"Run fuzzers","conclusion":"failure","started_at":"2026-11-20T11:00:59Z","completed_at":"2026-11-20T11:01:33Z"}]}]}
JSON
{
  printf 'batch fuzzing\tUNKNOWN STEP\t2026-11-20T11:01:00.6808922Z fatal: unable to access %s: The requested URL returned error: 403\n' \
    "'https://github.com/openartifact/openartifact-fuzz-storage.git/'"
  printf 'batch fuzzing\tUNKNOWN STEP\t2026-11-20T11:01:01.7546278Z fatal: unable to access %s: The requested URL returned error: 403\n' \
    "'https://github.com/openartifact/openartifact-fuzz-storage.git/'"
  printf 'batch fuzzing\tUNKNOWN STEP\t2026-11-20T11:01:32.0421255Z 2026-11-20 11:01:32,041 - root - ERROR - Retrying on GitFilestore._clone failed with Command returned non-zero exit status 128.\n'
  printf 'batch fuzzing\tUNKNOWN STEP\t2026-11-20T11:01:32.0422164Z Traceback (most recent call last):\n'
  printf 'batch fuzzing\tUNKNOWN STEP\t2026-11-20T11:01:32.0422999Z   File "/usr/local/lib/python3.10/site-packages/retry/api.py", line 33, in __retry_internal\n'
  printf 'batch fuzzing\tUNKNOWN STEP\t2026-11-20T11:01:32.0458422Z subprocess.CalledProcessError: Command returned non-zero exit status 128.\n'
  printf 'batch fuzzing\tUNKNOWN STEP\t2026-11-20T11:01:33.1000000Z ##[error]Process completed with exit code 1.\n'
} > "$fix/log"
echo "$open_issue" > "$fix/issues.json"

calls=$(run failure)
has "infrastructure failure: comments on the open issue" "gh issue comment 42" "$calls"
has "infrastructure failure: names the failed step" 'step "Run fuzzers" failure after 0 min' "$calls"
has "infrastructure failure: shows the git error" "fatal: unable to access" "$calls"
has "infrastructure failure: shows the ClusterFuzzLite error" "root - ERROR - Retrying on GitFilestore._clone failed" "$calls"
has "infrastructure failure: shows the traceback" "Traceback (most recent call last)" "$calls"
has "infrastructure failure: shows the exception" "subprocess.CalledProcessError" "$calls"
has "infrastructure failure: shows the failed step line" "##[error]Process completed with exit code 1." "$calls"
hasnt "infrastructure failure: does not say there is nothing" "no crash or error lines" "$calls"
check "infrastructure failure: the repeated git error is listed once" "1" \
  "$(grep -c '^fatal: unable to access' <<< "$calls")"

# A crash, with no issue yet.
cat > "$fix/jobs.json" << 'JSON'
{"total_count":1,"jobs":[
 {"id":136812531724,"name":"batch fuzzing","conclusion":"failure","steps":[
  {"name":"Run fuzzers","conclusion":"failure","started_at":"2026-11-21T11:59:00Z","completed_at":"2026-11-21T13:30:00Z"}]}]}
JSON
{
  printf 'batch fuzzing\tUNKNOWN STEP\t2026-11-21T11:59:22.2898117Z panic: gems/x.gem?; served the wrong gem\n'
  printf 'batch fuzzing\tUNKNOWN STEP\t2026-11-21T11:59:22.2898118Z SUMMARY: libFuzzer: deadly signal\n'
  printf 'batch fuzzing\tUNKNOWN STEP\t2026-11-21T11:59:22.2898119Z Base64: Z2Vtcy94Lmdlbj87\n'
  printf 'batch fuzzing\tUNKNOWN STEP\t2026-11-21T11:59:22.2898120Z unrelated log line\n'
} > "$fix/log"
echo "$no_issue" > "$fix/issues.json"

calls=$(run failure)
has "crash: opens the issue" "gh issue create" "$calls"
has "crash: shows the panic" "panic: gems/x.gem?; served the wrong gem" "$calls"
has "crash: shows the input" "Base64: Z2Vtcy94Lmdlbj87" "$calls"
hasnt "crash: leaves out the rest of the log" "unrelated log line" "$calls"

# A failure with nothing in the log still says which job failed.
: > "$fix/log"
calls=$(run failure)
has "silent failure: names the job" 'job "batch fuzzing" failure' "$calls"
has "silent failure: says the log has nothing" "no crash or error lines found" "$calls"

# A passing run closes the issue, if there is one.
echo "$open_issue" > "$fix/issues.json"
has "success: closes the open issue" "gh issue close 42" "$(run success)"
echo "$no_issue" > "$fix/issues.json"
check "success: nothing to close" "" "$(run success)"

exit $fail
