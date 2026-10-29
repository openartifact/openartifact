#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# rpm: rpmbuild builds a package, the upload API stores it, dnf installs it from
# the generated repository. Image: RPM_IMAGE (default fedora:latest, the newest
# dnf; RPM_IMAGE=rockylinux:9 checks the enterprise client).
# Usage: OA_TOKEN=... scripts/smoke/rpm.sh [URL]
# shellcheck source=lib.sh
. "$(dirname "$0")/lib.sh"
smoke_init rpm fedora:latest "$@"

smoke_run "$(cat <<'SCRIPT'
set -eu
dnf --version | head -1
cat > /etc/yum.repos.d/smoke.repo <<EOF
[smoke]
name=smoke
baseurl=$OA_URL/repos/$REPO
enabled=1
gpgcheck=0
repo_gpgcheck=0
username=smoke
password=$OA_TOKEN
metadata_expire=1
EOF
[ "$SMOKE_MODE" = install ] || {
  dnf -y -q --disablerepo=smoke install rpm-build >/dev/null
  cat > /tmp/$PKG.spec <<EOF
Name: $PKG
Version: $VERSION
Release: 1
Summary: smoke test package
License: MIT
BuildArch: noarch
%description
smoke test package
%install
mkdir -p %{buildroot}/usr/bin
printf "#!/bin/sh\necho $PKG\n" > %{buildroot}/usr/bin/$PKG
chmod 755 %{buildroot}/usr/bin/$PKG
%files
/usr/bin/$PKG
EOF
  rpmbuild -bb --define "_topdir /tmp/rpmbuild" /tmp/$PKG.spec >/dev/null 2>&1
  f=$(find /tmp/rpmbuild/RPMS -name "$PKG-*.rpm" | head -1)
  [ -n "$f" ] || { echo "rpmbuild produced nothing"; exit 1; }
  curl -fsS -H "Authorization: Bearer $OA_TOKEN" -F file=@"$f" "$OA_URL/api/v1/repositories/$REPO/packages" >/dev/null
}
[ "$SMOKE_MODE" = publish ] || {
  # every attempt starts from cleaned dnf metadata and caches
  i=0
  until dnf -q clean all >/dev/null 2>&1; dnf -q --disablerepo="*" --enablerepo=smoke list available $PKG >/dev/null 2>&1; do
    i=$((i+1)); [ $i -lt 30 ] || { echo "package never appeared in the rpm index"; exit 1; }; sleep 1
  done
  dnf -y -q --disablerepo="*" --enablerepo=smoke install $PKG >/dev/null
  [ "$(/usr/bin/$PKG)" = "$PKG" ]
  rpm -q $PKG | grep -q "$VERSION"
}
SCRIPT
)"
