#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# deb: dpkg-deb builds a package, the upload API stores it, apt installs it from
# the generated repository. Image: DEB_IMAGE (default debian:stable).
# Usage: OA_TOKEN=... scripts/smoke/deb.sh [URL]
# shellcheck source=lib.sh
. "$(dirname "$0")/lib.sh"
smoke_init deb debian:stable "$@"

smoke_run "$(cat <<'SCRIPT'
set -eu
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq && apt-get install -y -qq curl ca-certificates >/dev/null
apt-get --version | head -1
echo "deb [trusted=yes] $OA_URL/repos/$REPO stable main" > /etc/apt/sources.list.d/smoke.list
printf "machine %s/ login smoke password %s\n" "$OA_URL" "$OA_TOKEN" > /etc/apt/auth.conf.d/smoke.conf
chmod 600 /etc/apt/auth.conf.d/smoke.conf
[ "$SMOKE_MODE" = install ] || {
  d=/tmp/pkg/$PKG; mkdir -p $d/DEBIAN $d/usr/bin
  printf "#!/bin/sh\necho %s\n" "$PKG" > $d/usr/bin/$PKG; chmod 755 $d/usr/bin/$PKG
  cat > $d/DEBIAN/control <<EOF
Package: $PKG
Version: $VERSION
Architecture: all
Maintainer: smoke <smoke@example.com>
Description: smoke test package
EOF
  dpkg-deb --build $d /tmp/$PKG.deb >/dev/null
  curl -fsS -H "Authorization: Bearer $OA_TOKEN" -F file=@/tmp/$PKG.deb -F distribution=stable -F component=main \
    "$OA_URL/api/v1/repositories/$REPO/debs" >/dev/null
}
[ "$SMOKE_MODE" = publish ] || {
  # fresh apt state: no package lists, no cached archives
  rm -rf /var/lib/apt/lists/* && apt-get clean
  i=0
  until apt-get update -qq >/dev/null 2>&1 && apt-cache policy $PKG | grep -q "Candidate: $VERSION"; do
    i=$((i+1)); [ $i -lt 30 ] || { echo "package never appeared in the apt index"; exit 1; }; sleep 1
  done
  apt-get install -y -qq $PKG >/dev/null
  [ "$(/usr/bin/$PKG)" = "$PKG" ]
  dpkg -s $PKG | grep -q "^Version: $VERSION"
}
SCRIPT
)"
