#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Keep one tracking issue per nightly fuzz workflow: a failed run opens it (or
# comments on it while it is open) with the crashing target, the failing input
# and the run link; the next passing run comments and closes it. A cancelled
# run changes nothing.
#
# Usage: scripts/fuzz-issue.sh WORKFLOW-NAME RESULT
#   RESULT  success | failure | cancelled  (the aggregate of the fuzz jobs)
# Environment: GH_TOKEN, GITHUB_REPOSITORY, GITHUB_RUN_ID, GITHUB_SERVER_URL.
#   DRY_RUN=1  print what would be done instead of calling the API.
#   LOG_FILE   read the failed log from this file instead of `gh run view`.
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
# the failing input and the seed, without ANSI colours and the log prefix.
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
			'^(panic:|fatal error:|--- FAIL: |FAIL\s+github\.com|SUMMARY: |Base64: |Seed: |Reproduce with|api-fuzz: (mode=|=== pass)|[0-9]+\. Test Case ID|- [A-Z]|\[[0-9]{3}\] |\s+(Expected|Received|Documented|curl -X)|_{3,} )|Test unit written to|Uploading crashes in|ERROR: (AddressSanitizer|libFuzzer)' |
		awk '!seen[$0]++ && n++ < 40 { print substr($0, 1, 300) }' ||
		true
}

existing="$(gh issue list --repo "$repo" --state open --search "\"$title\" in:title" --json number,title \
	--jq "[.[] | select(.title == \"$title\")][0].number // empty")"

case "$result" in
failure)
	details="$(failure_details)"
	body="The $workflow run failed: $run_url

Crash details from the failed log (the full log and any uploaded crash artifacts are on the run page):

\`\`\`text
${details:-no crash lines found in the failed log: open the run}
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
