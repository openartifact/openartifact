#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later

# Dead-code gate. Runs golang.org/x/tools/cmd/deadcode (pinned in
# tools/go.mod) over every main package, with the integration build tag, and
# compares the result with the committed allowlist scripts/deadcode-baseline.txt.
#
# Why no -test: with -test every test function is a root, so a production
# function that only tests call counts as live. Without it such functions are
# reported, which is what we want: test-only helpers belong in _test.go files
# or a *test support package, not in production files. Everything -test would
# report is a subset of this output.
#
# Usage:
#   scripts/deadcode-check.sh            fail on unreachable code not in the baseline
#                                        (and on baseline entries that no longer match)
#   scripts/deadcode-check.sh --update   rewrite the baseline: drop stale entries,
#                                        append new ones with a REASON REQUIRED
#                                        placeholder that you must replace
#
# Baseline format (docs/development/README.md#dead-code-gate):
#   - one entry per line: "<file path> <symbol>"; a path ending in "/" matches
#     every file below that directory and the symbol "*" matches any symbol
#   - symbol is what deadcode prints: "Func" or "Type.Method"
#   - every group of entries (up to the next blank line) needs a "#" comment
#     above it giving the reason it is intentionally kept
#   - line numbers are never recorded, so edits above a symbol do not churn it

set -euo pipefail

cd "$(dirname "$0")/.."

baseline=scripts/deadcode-baseline.txt
update=0

case "${1:-}" in
"") ;;
--update) update=1 ;;
-h | --help)
	sed -n '/^# Usage:/,/^# Baseline format/p' "$0" | sed '$d; s/^# \{0,1\}//'
	exit 0
	;;
*)
	echo "usage: $0 [--update]" >&2
	exit 2
	;;
esac

export LC_ALL=C

tmp=$(mktemp -d "${TMPDIR:-/tmp}/deadcode.XXXXXX")
trap 'rm -rf "$tmp"' EXIT

# 1. Run deadcode and reduce each report to "<file>\t<symbol>" (sorted, unique).
go tool -modfile=tools/go.mod deadcode -tags integration ./... >"$tmp/raw"

awk '
	/^[[:space:]]*$/ { next }
	match($0, /^[^:]+:[0-9]+:[0-9]+: unreachable func: /) {
		loc = substr($0, 1, RLENGTH)
		sym = substr($0, RLENGTH + 1)
		sub(/:[0-9]+:[0-9]+: unreachable func: $/, "", loc)
		print loc "\t" sym
		next
	}
	{ print "deadcode-check: unrecognised deadcode output line: " $0 > "/dev/stderr"; bad = 1 }
	END { exit bad }
' "$tmp/raw" | sort -u >"$tmp/reported"

if [ ! -f "$baseline" ]; then
	: >"$tmp/entries"
else
	# 2. Parse the baseline into "<path>\t<symbol>" entries, validating reasons.
	awk -v strict=$((1 - update)) '
		/^[[:space:]]*$/ { reason = 0; next }
		/^#/ {
			if ($0 ~ /REASON REQUIRED/) { placeholder = 1 } else { reason = 1 }
			next
		}
		{
			if (NF != 2) {
				printf "%s:%d: expected \"<file> <symbol>\", got: %s\n", FILENAME, FNR, $0 > "/dev/stderr"
				bad = 1
				next
			}
			if (strict && !reason) {
				printf "%s:%d: entry has no reason comment above it: %s\n", FILENAME, FNR, $0 > "/dev/stderr"
				bad = 1
			}
			print $1 "\t" $2
		}
		END {
			if (strict && placeholder) {
				print FILENAME ": a REASON REQUIRED placeholder is still present; replace it with the reason" > "/dev/stderr"
				bad = 1
			}
			exit bad
		}
	' "$baseline" | sort -u >"$tmp/entries"
fi

# 3. Match reported symbols against entries: NEW = reported but not allowed,
#    STALE = allowed but no longer reported.
awk -F'\t' '
	FILENAME == ARGV[1] { path[++n] = $1; sym[n] = $2; next }
	{
		matched = 0
		for (i = 1; i <= n; i++) {
			p = path[i]
			if (substr(p, length(p)) == "/") { ok = (index($1, p) == 1) } else { ok = ($1 == p) }
			if (ok && (sym[i] == "*" || sym[i] == $2)) { matched = 1; used[i] = 1 }
		}
		if (!matched) print "NEW\t" $1 "\t" $2
	}
	END { for (i = 1; i <= n; i++) if (!used[i]) print "STALE\t" path[i] "\t" sym[i] }
' "$tmp/entries" "$tmp/reported" | sort >"$tmp/diff"

new=$(awk -F'\t' '$1 == "NEW" { print "  " $2 "  " $3 }' "$tmp/diff")
stale=$(awk -F'\t' '$1 == "STALE" { print "  " $2 "  " $3 }' "$tmp/diff")

if [ "$update" = 1 ]; then
	[ -f "$baseline" ] || : >"$baseline"

	# Keep every comment and entry that is still valid, drop stale entries
	# (and the comment of a group whose entries are all stale), append new ones.
	awk -F'\t' '
		FILENAME == ARGV[1] { if ($1 == "STALE") stale[$2 "\t" $3] = 1; next }
		function flush() {
			if (buf != "" && (!had || kept)) printf "%s", buf
			buf = ""; had = 0; kept = 0
		}
		/^[[:space:]]*$/ { flush(); print ""; next }
		/^#/ { buf = buf $0 "\n"; next }
		{
			had = 1
			split($0, f, /[ \t]+/)
			if (!((f[1] "\t" f[2]) in stale)) { kept = 1; buf = buf $0 "\n" }
		}
		END { flush() }
	' "$tmp/diff" "$baseline" >"$tmp/updated.raw"

	# Drop trailing blank lines, then append the new group after one blank line.
	awk 'NF { for (; blank > 0; blank--) print ""; print; next } { blank++ }' "$tmp/updated.raw" >"$tmp/updated"

	if [ -n "$new" ]; then
		{
			[ -s "$tmp/updated" ] && echo
			echo "# REASON REQUIRED: say why these are intentionally kept (prefer deleting dead code"
			echo "# or moving test-only helpers into a _test.go file)."
			awk -F'\t' '$1 == "NEW" { print $2 " " $3 }' "$tmp/diff"
		} >>"$tmp/updated"
	fi

	cp "$tmp/updated" "$baseline"
	echo "deadcode-check: wrote $baseline ($(grep -cv '^[[:space:]]*\(#\|$\)' "$baseline") entries)"
	[ -n "$new" ] && echo "deadcode-check: fill in the REASON REQUIRED comment for the new entries"
	exit 0
fi

status=0

if [ -n "$new" ]; then
	status=1
	cat <<EOF
deadcode-check: new unreachable code (no main package reaches it, and it is not in $baseline):

$new

Fix it in this order:
  1. Dead everywhere: delete it.
  2. Only used by tests: move it into a _test.go file (or the shared *test
     support package for that area); do not keep test helpers in production files.
  3. Reached only through reflection, templates, go:linkname, a build tag the
     gate does not set, or kept as a deliberate public/test seam: run
     scripts/deadcode-check.sh --update, then replace the REASON REQUIRED
     comment above the new entries with the actual reason.

EOF
fi

if [ -n "$stale" ]; then
	status=1
	cat <<EOF
deadcode-check: stale entries in $baseline (symbol is gone or now reachable):

$stale

Remove them (scripts/deadcode-check.sh --update does it).

EOF
fi

if [ "$status" = 0 ]; then
	echo "deadcode-check: ok ($(wc -l <"$tmp/reported" | tr -d ' ') unreachable symbols, all allowlisted in $baseline)"
else
	echo "See docs/development/README.md#dead-code-gate" >&2
fi

exit "$status"
