#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later

# Lists the Go packages of one integration shard, one ./path per line. Every
# package with an integration-tagged test file belongs to exactly one shard, so a
# new one can't be missed: `rest` is whatever the named shards do not claim.
# The CI job `integration-shard` (ci.yml) runs one shard per matrix entry; the
# names are its matrix values.
#
# Usage: scripts/integration-packages.sh SHARD
#   SHARD  database | worker | tests | rest | all
#
#   database  internal/database
#   worker    internal/worker, internal/federation (background work)
#   tests     ./tests (the HTTP/handler suite, not tests/integration)
#   rest      everything else, including tests/integration (testcontainers)
#   all       every package (what `make test-integration` approximates)
#
# Shards are balanced by the test time of CI run 40864231186 (package wall times
# in seconds under contention: tests 632, database 550, worker+federation 603,
# the remaining packages 577). Move a package between shards by editing
# claim() below.

set -euo pipefail

if [ "$#" -ne 1 ]; then
	echo "usage: $0 database|worker|tests|rest|all" >&2
	exit 2
fi
shard=$1

root=$(git -C "$(dirname "$0")" rev-parse --show-toplevel)
cd "$root"

# claim <package dir>: the named shard (not rest) that runs the package, or "".
claim() {
	case "$1" in
	internal/database | internal/database/*) echo database ;;
	internal/worker | internal/worker/* | internal/federation | internal/federation/*) echo worker ;;
	tests) echo tests ;;
	esac
}

# Every package with an integration-tagged test file.
pkgs=$(grep -rl --include='*_test.go' -E '^//go:build .*integration' cmd internal pkg tests |
	xargs -n1 dirname | sort -u)

case "$shard" in
database | worker | tests | rest | all) ;;
*)
	echo "unknown shard '$shard' (database, worker, tests, rest, all)" >&2
	exit 2
	;;
esac

while IFS= read -r dir; do
	[ -n "$dir" ] || continue
	owner=$(claim "$dir")
	[ -n "$owner" ] || owner=rest
	if [ "$shard" = all ] || [ "$shard" = "$owner" ]; then
		echo "./$dir"
	fi
done <<<"$pkgs"
