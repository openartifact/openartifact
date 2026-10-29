#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# raw: curl -T upload to the repository, then listing, download and sha256 header.
# Image: RAW_IMAGE (default alpine/curl:latest, the newest curl).
# Usage: OA_TOKEN=... scripts/smoke/raw.sh [URL]
# shellcheck source=lib.sh
. "$(dirname "$0")/lib.sh"
smoke_init raw alpine/curl:latest "$@"

smoke_run "$(cat <<'SCRIPT'
set -eu
curl --version | head -n1
base="$OA_URL/repos/raw/$REPO"
echo "marker $PKG $VERSION" > /tmp/f.txt
want="$(sha256sum /tmp/f.txt | cut -d' ' -f1)"
[ "$SMOKE_MODE" = install ] || {
  curl -fsS -u "smoke:$OA_TOKEN" -T /tmp/f.txt "$base/dir/$PKG.txt" >/dev/null
}
[ "$SMOKE_MODE" = publish ] || {
  curl -fsS -u "smoke:$OA_TOKEN" "$base/dir/$PKG.txt" -o /tmp/g.txt -D /tmp/h.txt
  cmp /tmp/f.txt /tmp/g.txt
  got="$(tr -d '\r' < /tmp/h.txt | grep -i '^x-checksum-sha256:' | cut -d' ' -f2)"
  [ "$got" = "$want" ]
  curl -fsS -H "Authorization: Bearer $OA_TOKEN" "$base/dir/" | grep -q "$want"
  [ "$(curl -s -o /dev/null -w '%{http_code}' "$base/dir/$PKG.txt")" != 200 ]
}
SCRIPT
)"
