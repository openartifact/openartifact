#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# pacman: makepkg builds a package, PUT stores it, pacman installs it from the
# generated database. Without a server GPG key the repo is unsigned and
# SigLevel is Never. Image: PACMAN_IMAGE (default archlinux:latest).
# Usage: OA_TOKEN=... scripts/smoke/pacman.sh [URL]
# shellcheck source=lib.sh
. "$(dirname "$0")/lib.sh"
smoke_init pacman archlinux:latest "$@"
SMOKE_SHELL=bash

smoke_run "$(cat <<'SCRIPT'
set -eu
pacman -Sy --noconfirm --needed base-devel curl >/dev/null 2>&1
pacman --version | sed -n 2p
useradd -m builder
mkdir -p /home/builder/pkg
cat > /home/builder/pkg/PKGBUILD <<EOF
pkgname=$PKG
pkgver=$VERSION
pkgrel=1
pkgdesc="smoke test package"
arch=(any)
license=(MIT)
package() {
	install -d "\$pkgdir"/usr/bin
	printf '#!/bin/sh\necho %s\n' "$PKG" > "\$pkgdir"/usr/bin/$PKG
	chmod 755 "\$pkgdir"/usr/bin/$PKG
}
EOF
chown -R builder /home/builder/pkg
su builder -c 'cd ~/pkg && PKGEXT=.pkg.tar.zst makepkg -f --nodeps >/dev/null 2>&1' ||
  { su builder -c 'cd ~/pkg && makepkg -f --nodeps'; exit 1; }
f=$(find /home/builder/pkg -name "$PKG-*.pkg.tar.zst" | head -1)
[ -n "$f" ] || { echo "makepkg produced nothing"; exit 1; }
curl -fsS -T "$f" -H "Authorization: Bearer $OA_TOKEN" "$OA_URL/repos/$REPO/os/x86_64/$(basename "$f")" >/dev/null
cred="smoke:$OA_TOKEN"
sig=Never
if curl -fsS -H "Authorization: Bearer $OA_TOKEN" -o /tmp/public.key "$OA_URL/repos/$REPO/public.key" 2>/dev/null; then
  pacman-key --init >/dev/null 2>&1
  pacman-key --add /tmp/public.key >/dev/null 2>&1
  fpr=$(gpg --show-keys --with-colons /tmp/public.key | awk -F: '/^fpr/{print $10; exit}')
  pacman-key --lsign-key "$fpr" >/dev/null 2>&1
  sig=Required
fi
echo "signature policy: $sig"
cat >> /etc/pacman.conf <<EOF
[$REPO]
SigLevel = $sig
Server = ${OA_URL%%://*}://$cred@${OA_URL#*://}/repos/$REPO/os/\$arch
EOF
i=0
until pacman -Sy >/dev/null 2>&1 && pacman -Si "$REPO/$PKG" >/dev/null 2>&1; do
  i=$((i+1)); [ $i -lt 30 ] || { echo "package never appeared in the pacman database"; pacman -Sy; exit 1; }; sleep 1
done
pacman -S --noconfirm "$REPO/$PKG" >/dev/null
[ "$(/usr/bin/$PKG)" = "$PKG" ]
pacman -Q "$PKG" | grep -q "$VERSION-1"
SCRIPT
)"
