#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# apk: abuild builds a native and a noarch package, the upload API stores it, apk installs it from
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
for variant in native noarch; do
  d=/home/builder/$variant; mkdir -p $d && chown builder $d
  if [ $variant = native ]; then name=$PKG; carch=all; else name=$PKG-doc; carch=noarch; fi
  cat > $d/APKBUILD <<EOF
pkgname=$name
pkgver=$VERSION
pkgrel=0
pkgdesc="smoke test package"
url="https://example.com"
arch="$carch"
license="MIT"
options="!check"
package() {
	mkdir -p "\$pkgdir"/usr/bin "\$pkgdir"/usr/share/$name
	printf '#!/bin/sh\necho %s\n' "$name" > "\$pkgdir"/usr/bin/$name
	chmod 755 "\$pkgdir"/usr/bin/$name
	echo $name > "\$pkgdir"/usr/share/$name/marker
}
EOF
  chown builder $d/APKBUILD
done
su builder -c 'abuild-keygen -a -n -q' >/dev/null 2>&1
cp /home/builder/.abuild/*.pub /etc/apk/keys/
for variant in native noarch; do
  su builder -c "cd ~/$variant && abuild -F -d -q" >/dev/null 2>&1 ||
    { su builder -c "cd ~/$variant && abuild -F -d"; exit 1; }
done
arch=$(apk --print-arch)
for name in $PKG $PKG-doc; do
  f=$(find /home/builder/packages -name "$name-$VERSION-*.apk" | head -1)
  [ -n "$f" ] || { echo "abuild produced no $name"; exit 1; }
  curl -fsS -T "$f" -H "Authorization: Bearer $OA_TOKEN" "$OA_URL/repos/$REPO/v3.20/main/$arch/$name-$VERSION-r0.apk" >/dev/null
done
keyname=$(curl -fsS -H "Authorization: Bearer $OA_TOKEN" "$OA_URL/api/v1/repositories/$REPO/apk-key" | jq -r .key_name)
curl -fsS -H "Authorization: Bearer $OA_TOKEN" -o "/etc/apk/keys/$keyname" "$OA_URL/repos/$REPO/public.key"
cred="smoke:$OA_TOKEN"
echo "$OA_URL/repos/$REPO/v3.20/main" | sed "s#://#://$cred@#" > /tmp/repositories
i=0
until apk update --repositories-file /tmp/repositories >/dev/null 2>&1 && apk --repositories-file /tmp/repositories search -x "$PKG" | grep -q "$PKG-$VERSION"; do
  i=$((i+1)); [ $i -lt 30 ] || { echo "package never appeared in the apk index"; apk update --repositories-file /tmp/repositories; exit 1; }; sleep 1
done
apk add -q --repositories-file /tmp/repositories "$PKG" "$PKG-doc"
[ "$(/usr/bin/$PKG)" = "$PKG" ]
# the noarch package: the index lists it under the directory arch, so apk fetches it from there
[ "$(cat /usr/share/$PKG-doc/marker)" = "$PKG-doc" ]
apk info -e "$PKG" "$PKG-doc"
SCRIPT
)"
