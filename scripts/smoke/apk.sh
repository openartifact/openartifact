#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# apk: abuild builds a package, the upload API stores it, apk installs it from
# the generated repository with the repository key verified. Image: APK_IMAGE
# (default alpine:latest).
# Usage: OA_TOKEN=... scripts/smoke/apk.sh [URL]
# shellcheck source=lib.sh
. "$(dirname "$0")/lib.sh"
smoke_init apk alpine:latest "$@"

smoke_run "$(cat <<'SCRIPT'
set -eu
apk add -q --no-cache curl jq abuild >/dev/null
apk --version
adduser -D builder && addgroup builder abuild
mkdir -p /home/builder/src && chown builder /home/builder/src
cat > /home/builder/src/APKBUILD <<EOF
pkgname=$PKG
pkgver=$VERSION
pkgrel=0
pkgdesc="smoke test package"
url="https://example.com"
arch="all"
license="MIT"
options="!check"
package() {
	mkdir -p "\$pkgdir"/usr/bin
	printf '#!/bin/sh\necho %s\n' "$PKG" > "\$pkgdir"/usr/bin/$PKG
	chmod 755 "\$pkgdir"/usr/bin/$PKG
}
EOF
chown builder /home/builder/src/APKBUILD
su builder -c 'abuild-keygen -a -n -q' >/dev/null 2>&1
cp /home/builder/.abuild/*.pub /etc/apk/keys/
su builder -c 'cd ~/src && abuild -F -d -q' >/dev/null 2>&1 ||
  { su builder -c 'cd ~/src && abuild -F -d'; exit 1; }
f=$(find /home/builder/packages -name "$PKG-*.apk" | head -1)
[ -n "$f" ] || { echo "abuild produced nothing"; exit 1; }
arch=$(apk --print-arch)
curl -fsS -T "$f" -H "Authorization: Bearer $OA_TOKEN" "$OA_URL/repos/$REPO/v3.20/main/$arch/$PKG-$VERSION-r0.apk" >/dev/null
keyname=$(curl -fsS -H "Authorization: Bearer $OA_TOKEN" "$OA_URL/api/v1/repositories/$REPO/apk-key" | jq -r .key_name)
curl -fsS -H "Authorization: Bearer $OA_TOKEN" -o "/etc/apk/keys/$keyname" "$OA_URL/repos/$REPO/public.key"
cred="smoke:$OA_TOKEN"
echo "$OA_URL/repos/$REPO/v3.20/main" | sed "s#://#://$cred@#" > /tmp/repositories
i=0
until apk update --repositories-file /tmp/repositories >/dev/null 2>&1 && apk --repositories-file /tmp/repositories search -x "$PKG" | grep -q "$PKG-$VERSION"; do
  i=$((i+1)); [ $i -lt 30 ] || { echo "package never appeared in the apk index"; apk update --repositories-file /tmp/repositories; exit 1; }; sleep 1
done
apk add -q --repositories-file /tmp/repositories "$PKG"
[ "$(/usr/bin/$PKG)" = "$PKG" ]
apk info -e "$PKG"
SCRIPT
)"
