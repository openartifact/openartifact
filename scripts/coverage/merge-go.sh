#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later

# Merge Go text coverage profiles (same -covermode, same -coverpkg) into one and
# drop code that is not ours to cover: sqlc output, the bench harness and the
# docgen fixtures. Counts of identical blocks are summed (atomic/count) or
# OR-ed (set).
#
# Usage: scripts/coverage/merge-go.sh OUT PROFILE...
#        scripts/coverage/merge-go.sh - PROFILE...   # write to stdout
#
# EXCLUDE_RE overrides the default exclusion regex (matched against the file
# path inside the profile).

set -euo pipefail

if [ "$#" -lt 2 ]; then
	echo "usage: $0 OUT PROFILE..." >&2
	exit 2
fi

out=$1
shift

exclude=${EXCLUDE_RE:-'/internal/database/dbsqlc/|/bench/|/cmd/docgen/testdata/'}

merged=$(
	awk '
		FNR == 1 {
			if ($1 != "mode:") { print "not a Go coverage profile: " FILENAME > "/dev/stderr"; bad = 1; exit 1 }
			if (mode != "" && mode != $2) { print "mixed coverage modes: " mode " vs " $2 > "/dev/stderr"; bad = 1; exit 1 }
			mode = $2
			next
		}
		NF == 3 {
			key = $1 " " $2
			if (mode == "set") { if ($3 > 0) cnt[key] = 1; else if (!(key in cnt)) cnt[key] = 0 }
			else cnt[key] += $3
		}
		END {
			if (bad) exit 1
			print "mode: " mode
			for (k in cnt) print k " " cnt[k]
		}
	' "$@" | { IFS= read -r header; echo "$header"; sort; }
)

# The profile path is module/import-path/file.go:range, so a leading "/" anchor
# in EXCLUDE_RE matches the directory separators inside it.
filtered=$(printf '%s\n' "$merged" | awk -v re="$exclude" 'NR == 1 || $1 !~ re')

if [ "$out" = "-" ]; then
	printf '%s\n' "$filtered"
else
	printf '%s\n' "$filtered" >"$out"
fi
