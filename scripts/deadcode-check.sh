#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later

# Dead-code gate: fails when golang.org/x/tools/cmd/deadcode (pinned in
# tools/go.mod) finds a function that no main package reaches. There is no
# allowlist; the expected result is zero findings.
#
# It runs with -tags integration and without -test: with -test every test
# function is a root, so a production function that only tests call would count
# as live. Without it such functions are reported, which is the point: test-only
# helpers belong in _test.go files or in a *test support package.
#
# The one exclusion is the Go convention for test support packages (httptest,
# iotest, ...): a package of this module whose directory name ends in "test" and
# that no other package imports from non-test code, apart from other such
# packages. Only tests import them, so deadcode reports all of their contents.
# A production package that merely ends in "test" (internal/attest) is imported
# by production code and is therefore still checked.
#
# Usage: scripts/deadcode-check.sh

set -euo pipefail

cd "$(dirname "$0")/.."

if [ "$#" -ne 0 ]; then
	echo "usage: $0" >&2
	exit 2
fi

export LC_ALL=C

tmp=$(mktemp -d "${TMPDIR:-/tmp}/deadcode.XXXXXX")
trap 'rm -rf "$tmp"' EXIT

module=$(go list -m)

# Test support packages, as module-relative directories (one per line).
go list -tags integration -f '{{.ImportPath}} {{join .Imports " "}}' ./... |
	awk -v module="$module" '
		{
			pkg = $1
			pkgs[pkg] = 1
			for (i = 2; i <= NF; i++) imp[pkg " " $i] = 1
		}
		function istest(p,   n, parts) {
			n = split(p, parts, "/")
			return parts[n] ~ /test$/
		}
		END {
			for (p in pkgs) {
				if (!istest(p)) continue
				used = 0
				for (k in imp) {
					split(k, kv, " ")
					if (kv[2] == p && !istest(kv[1])) { used = 1; break }
				}
				if (!used) print substr(p, length(module) + 2)
			}
		}
	' | sort >"$tmp/support"

go tool -modfile=tools/go.mod deadcode -tags integration ./... >"$tmp/raw"

# Reduce each report to "<file>  <symbol>" and drop test support packages.
awk -v supportfile="$tmp/support" '
	BEGIN { while ((getline d < supportfile) > 0) support[d] = 1 }
	/^[[:space:]]*$/ { next }
	match($0, /^[^:]+:[0-9]+:[0-9]+: unreachable func: /) {
		loc = substr($0, 1, RLENGTH)
		sym = substr($0, RLENGTH + 1)
		sub(/:[0-9]+:[0-9]+: unreachable func: $/, "", loc)
		dir = loc
		sub(/\/[^\/]*$/, "", dir)
		if (!(dir in support)) print loc "  " sym
		next
	}
	{ print "deadcode-check: unrecognised deadcode output line: " $0 > "/dev/stderr"; bad = 1 }
	END { exit bad }
' "$tmp/raw" | sort -u >"$tmp/findings"

if [ -s "$tmp/findings" ]; then
	cat <<EOF
deadcode-check: unreachable code (no main package reaches it):

$(sed 's/^/  /' "$tmp/findings")

Delete it, or move test-only code into a _test.go file or a *test package.
See docs/development/README.md#dead-code-gate
EOF
	exit 1
fi

echo "deadcode-check: ok"
