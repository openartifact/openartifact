#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later

# Fail when a total dropped by more than MAX_DROP percentage points (default 1.0)
# against the baseline coverage.json of main: the merged Go total and the web
# line coverage. No baseline file means nothing to compare and passes.
#
# Usage: scripts/coverage/gate.sh COVERAGE_JSON BASE_JSON

set -euo pipefail

if [ "$#" -ne 2 ]; then
	echo "usage: $0 COVERAGE_JSON BASE_JSON" >&2
	exit 2
fi

if [ ! -s "$2" ]; then
	echo "no baseline from main, coverage gate skipped"
	exit 0
fi

jq -r --slurpfile base "$2" --argjson max "${MAX_DROP:-1.0}" '
	[ {name: "Go total", cur: .go.total, old: $base[0].go.total},
	  {name: "Web lines", cur: .web.lines?, old: $base[0].web.lines?} ]
	| map(select(.cur != null and .old != null and (.old - .cur) > $max))
	| .[] | "\(.name) dropped from \(.old)% to \(.cur)% (more than \($max) points)"
' "$1" | {
	failed=0
	while IFS= read -r msg; do
		echo "::error title=Coverage drop::$msg"
		failed=1
	done
	exit "$failed"
}
