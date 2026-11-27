#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Keep one tracking issue per nightly fuzz workflow: a failed run opens it (or
# comments on it while it is open) with the failed jobs and steps, the crashing
# target, input and error lines of the log, and the run link; the next passing
# run comments and closes it. A run cancelled by someone changes nothing; one
# whose job hit its timeout is a failure (GitHub reports it as cancelled, and
# only the job's check-run annotation says why).
#
# Usage: scripts/fuzz-issue.sh WORKFLOW-NAME RESULT
#   RESULT  success | failure | cancelled  (the aggregate of the fuzz jobs)
# Environment: GH_TOKEN, GITHUB_REPOSITORY, GITHUB_RUN_ID, GITHUB_SERVER_URL.
#   DRY_RUN=1  print what would be done instead of calling the API.
#   LOG_FILE   read the failed log from this file instead of `gh run view`.
# Test: scripts/fuzz-issue_test.sh.
set -euo pipefail

workflow="${1:?usage: $0 WORKFLOW-NAME RESULT}"
result="${2:?usage: $0 WORKFLOW-NAME RESULT}"
repo="${GITHUB_REPOSITORY:?}"
run_id="${GITHUB_RUN_ID:?}"
run_url="${GITHUB_SERVER_URL:-https://github.com}/$repo/actions/runs/$run_id"
title="Nightly fuzz failure: $workflow"

gh_() {
	if [ -n "${DRY_RUN:-}" ]; then
		echo "DRY_RUN gh $*" >&2
	else
		gh "$@"
	fi
}

# failure_details: the lines of the failed log that name the crashing target,
# the failing input and the seed, or the error that stopped the run when it was
# no crash (a git or ClusterFuzzLite error, a Python traceback, a failed step),
# without ANSI colours and the log prefix.
failure_details() {
	local log
	if [ -n "${LOG_FILE:-}" ]; then
		log="$(cat "$LOG_FILE")"
	else
		log="$(gh run view "$run_id" --repo "$repo" --log-failed 2>/dev/null || true)"
	fi
	# Strip "job<TAB>step<TAB>timestamp " and escape sequences.
	printf '%s\n' "$log" |
		sed -E 's/\x1b\[[0-9;]*[A-Za-z]//g; s/^[^\t]*\t[^\t]*\t[0-9T:.Z-]+ ?//' |
		grep -aE \
			'^(panic:|fatal error:|fatal: |Traceback \(most recent call last\)|[A-Za-z_.]+(Error|Exception): |##\[error\]|--- FAIL: |FAIL\s+github\.com|SUMMARY: |Base64: |Seed: |Reproduce with|api-fuzz: (mode=|=== pass)|[0-9]+\. Test Case ID|- [A-Z]|\[[0-9]{3}\] |\s+(Expected|Received|Documented|curl -X)|_{3,} )|Test unit written to|Uploading crashes in|ERROR: (AddressSanitizer|libFuzzer)| - root - (ERROR|CRITICAL) - ' |
		awk '!seen[$0]++ && n++ < 40 { print substr($0, 1, 300) }' ||
		true
}

# run_jobs: the jobs of this run, one JSON object per line.
run_jobs() {
	gh api "repos/$repo/actions/runs/$run_id/jobs?per_page=100" --jq '.jobs[]' 2>/dev/null || true
}

# timed_out_jobs: the names of the jobs that hit their timeout. GitHub reports
# them as cancelled, like a cancel by hand; the job's check-run annotations are
# the only place that tells them apart.
timed_out_jobs() {
	local id name annotations
	while IFS=$'\t' read -r id name; do
		annotations="$(gh api "repos/$repo/check-runs/$id/annotations" --jq '.[].message' 2>/dev/null || true)"
		case "$annotations" in
		*"exceeded the maximum execution time"*) echo "$name" ;;
		esac
	done < <(run_jobs | jq -r 'select(.conclusion == "cancelled") | [.id, .name] | @tsv')
}

# failed_jobs: the failed and cancelled jobs with the steps that stopped them
# and how long those ran: when a job times out or fails before any crash, that
# is what the run page shows first. $timed_out lists the jobs that timed out.
failed_jobs() {
	run_jobs | jq -r --arg timed_out "$timed_out" '
		select(.conclusion == "failure" or .conclusion == "cancelled") | . as $job
		| "job \"\($job.name)\" \($job.conclusion)"
		  + (if ($timed_out | split("\n") | index($job.name)) != null then " (timed out)" else "" end),
		  ($job.steps[]? | select(.conclusion == "failure" or .conclusion == "cancelled")
		   | "  step \"\(.name)\" \(.conclusion)"
		     + (if .started_at and .completed_at
		        then " after \(((.completed_at | fromdateiso8601) - (.started_at | fromdateiso8601)) / 60 | floor) min"
		        else "" end))'
}

# A job that hit its timeout was not cancelled by anyone: it failed.
timed_out=""
if [ "$result" = cancelled ]; then
	timed_out="$(timed_out_jobs)"
	if [ -n "$timed_out" ]; then
		result=failure
	fi
fi

existing="$(gh issue list --repo "$repo" --state open --search "\"$title\" in:title" --json number,title \
	--jq "[.[] | select(.title == \"$title\")][0].number // empty")"

case "$result" in
failure)
	jobs_failed="$(failed_jobs)"
	details="$(failure_details)"
	body="The $workflow run failed: $run_url

Failed jobs and the steps that stopped them:

\`\`\`text
${jobs_failed:-no failed job found: open the run}
\`\`\`

Crash and error lines from the failed log (the full log and any uploaded crash artifacts are on the run page; a job that timed out has no failed step to read):

\`\`\`text
${details:-no crash or error lines found in the failed log: open the run}
\`\`\`

This issue is updated by each failing nightly run and closed by the next passing one."
	if [ -n "$existing" ]; then
		gh_ issue comment "$existing" --repo "$repo" --body "$body"
	else
		gh_ issue create --repo "$repo" --title "$title" --body "$body"
	fi
	;;
success)
	if [ -n "$existing" ]; then
		gh_ issue close "$existing" --repo "$repo" --comment "The $workflow run passed again: $run_url"
	fi
	;;
*)
	echo "fuzz-issue: result '$result': nothing to do"
	;;
esac
