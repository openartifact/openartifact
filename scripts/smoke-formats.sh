#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Publish a tiny package to a running OpenArtifact instance and install it back
# with the real client, one throwaway container per format. The client logic of
# each format lives in scripts/smoke/<format>.sh (helpers: scripts/smoke/lib.sh);
# this script only runs the selected ones one after the other, keeps going after
# a failure and fails at the end if any format failed. The nightly workflow
# .github/workflows/client-matrix.yml runs each format script as its own matrix
# entry against a fresh stack (scripts/smoke-stack.sh).
#
# Usage:
#   OA_TOKEN=$(OA_PASS=... scripts/dev-token.sh URL) scripts/smoke-formats.sh [URL]
#   eval "$(scripts/smoke-stack.sh up)" && scripts/smoke-formats.sh   # fresh stack
#
# Environment:
#   OA_URL       base URL (default http://127.0.0.1:8080; argument wins)
#   OA_TOKEN     access token of an administrator (required; repositories are
#                created and deleted through the API)
#   FORMATS      space-separated subset of the scripts/smoke/*.sh names
#                (default: all)
#   <FORMAT>_IMAGE  client image of one format, e.g. NPM_IMAGE=node:22-alpine
#                (the default of each format is in its script)
#   KEEP         1 keeps the repositories after the run
#
# Containers use the host network, so URL may be 127.0.0.1:PORT. The token
# reaches containers through the environment, never through a command line.
# Needs docker, curl and jq on the host and Internet access in the containers
# (the clients install their tools).
set -euo pipefail

die() { echo "smoke-formats: $*" >&2; exit 1; }

dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/smoke"
if [ -z "${FORMATS:-}" ]; then
	FORMATS=""
	for f in "$dir"/*.sh; do
		n="$(basename "$f" .sh)"
		[ "$n" = lib ] || FORMATS="$FORMATS $n"
	done
fi

failed=()
for f in $FORMATS; do
	[ -x "$dir/$f.sh" ] && [ "$f" != lib ] || die "unknown format: $f"
	if "$dir/$f.sh" "$@"; then
		echo "==> $f: OK"
	else
		echo "==> $f: FAILED" >&2
		failed+=("$f")
	fi
done

if [ "${#failed[@]}" -gt 0 ]; then
	die "failed: ${failed[*]}"
fi
echo "smoke-formats: all formats passed ($FORMATS)"
