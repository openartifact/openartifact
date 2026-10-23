#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Publish a tiny package to a running OpenArtifact instance and install it back
# with the real client, one throwaway container per format. Round trips: deb
# (dpkg-deb + apt), rpm (rpmbuild + dnf), npm (npm publish/install) and pypi
# (build + twine + pip). Every step must succeed; the first failure fails the
# run. Modelled on yap's scripts/e2e-rpm.sh.
#
# Usage:
#   OA_TOKEN=$(OA_PASS=... scripts/dev-token.sh URL) scripts/smoke-formats.sh [URL]
#
# Environment:
#   OA_URL       base URL (default http://127.0.0.1:8080; argument wins)
#   OA_TOKEN     access token of an administrator (required; repositories are
#                created and deleted through the API)
#   FORMATS      space-separated subset of "deb rpm npm pypi" (default: all)
#   DEB_IMAGE    default debian:12
#   RPM_IMAGE    default rockylinux:9
#   NODE_IMAGE   default node:22-alpine
#   PY_IMAGE     default python:3.12-slim
#   KEEP         1 keeps the repositories after the run
#
# Containers use the host network, so URL may be 127.0.0.1:PORT. The token
# reaches containers through the environment, never through a command line.
# Needs docker, curl and jq on the host and Internet access in the containers
# (apt, dnf, npm and pip install their tools).
# shellcheck disable=SC2016 # the container scripts are single-quoted on purpose
set -euo pipefail

die() { echo "smoke-formats: $*" >&2; exit 1; }

URL="${1:-${OA_URL:-http://127.0.0.1:8080}}"
URL="${URL%/}"
[ -n "${OA_TOKEN:-}" ] || die "set OA_TOKEN (see scripts/dev-token.sh)"
for c in docker curl jq; do command -v "$c" >/dev/null || die "$c is required"; done

FORMATS="${FORMATS:-deb rpm npm pypi}"
DEB_IMAGE="${DEB_IMAGE:-debian:12}"
RPM_IMAGE="${RPM_IMAGE:-rockylinux:9}"
NODE_IMAGE="${NODE_IMAGE:-node:22-alpine}"
PY_IMAGE="${PY_IMAGE:-python:3.12-slim}"

id="$(date +%s)$RANDOM"
created=()
failed=()

api() { # METHOD PATH [curl args...]
	local method="$1" path="$2"
	shift 2
	curl -sS -X "$method" "$URL$path" -H "Authorization: Bearer $OA_TOKEN" "$@"
}

create_repo() { # NAME FORMAT
	local code
	code="$(jq -n --arg n "$1" --arg f "$2" '{name:$n,format:$f,public:false}' |
		api POST /api/v1/repositories -H 'Content-Type: application/json' --data-binary @- \
			-o /dev/null -w '%{http_code}')"
	[ "$code" = 200 ] || [ "$code" = 201 ] || die "creating repository $1 failed (HTTP $code)"
	created+=("$1")
}

cleanup() {
	[ "${KEEP:-0}" = 1 ] && return
	local r
	for r in "${created[@]}"; do
		api DELETE "/api/v1/repository/$r?confirm=$r" -o /dev/null || true
	done
}
trap cleanup EXIT

# run NAME IMAGE REPO SCRIPT: run SCRIPT in a throwaway container.
run() {
	local name="$1" image="$2" repo="$3" script="$4"
	echo "==> $name: $image"
	if docker run --rm --network host -e OA_URL="$URL" -e OA_TOKEN -e REPO="$repo" \
		-e PKG="smoke${id}" -e VERSION=1.0.0 "$image" sh -c "$script"; then
		echo "==> $name: OK"
	else
		echo "==> $name: FAILED" >&2
		failed+=("$name")
	fi
}

DEB_SCRIPT='set -eu
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq && apt-get install -y -qq curl ca-certificates >/dev/null
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
echo "deb [trusted=yes] $OA_URL/repos/$REPO stable main" > /etc/apt/sources.list.d/smoke.list
printf "machine %s/ login smoke password %s\n" "$OA_URL" "$OA_TOKEN" > /etc/apt/auth.conf.d/smoke.conf
chmod 600 /etc/apt/auth.conf.d/smoke.conf
i=0
until apt-get update -qq >/dev/null 2>&1 && apt-cache policy $PKG | grep -q "Candidate: $VERSION"; do
  i=$((i+1)); [ $i -lt 30 ] || { echo "package never appeared in the apt index"; exit 1; }; sleep 1
done
apt-get install -y -qq $PKG >/dev/null
[ "$(/usr/bin/$PKG)" = "$PKG" ]
dpkg -s $PKG | grep -q "^Version: $VERSION"'

RPM_SCRIPT='set -eu
dnf -y -q install rpm-build >/dev/null
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
i=0
until dnf -q clean all >/dev/null 2>&1; dnf -q --disablerepo="*" --enablerepo=smoke list available $PKG >/dev/null 2>&1; do
  i=$((i+1)); [ $i -lt 30 ] || { echo "package never appeared in the rpm index"; exit 1; }; sleep 1
done
dnf -y -q --disablerepo="*" --enablerepo=smoke install $PKG >/dev/null
[ "$(/usr/bin/$PKG)" = "$PKG" ]
rpm -q $PKG | grep -q "$VERSION"'

NPM_SCRIPT='set -eu
hp="${OA_URL#*://}"
cat > /tmp/npmrc <<EOF
registry=$OA_URL/repos/$REPO/
//$hp/repos/$REPO/:_authToken=$OA_TOKEN
EOF
export NPM_CONFIG_USERCONFIG=/tmp/npmrc
mkdir -p /tmp/p /tmp/c && cd /tmp/p
printf "{\"name\":\"%s\",\"version\":\"%s\",\"main\":\"index.js\"}\n" "$PKG" "$VERSION" > package.json
echo "module.exports = \"$PKG\"" > index.js
npm publish >/dev/null
cd /tmp/c && npm init -y >/dev/null && npm install "$PKG@$VERSION" >/dev/null
[ "$(node -p "require(\"$PKG\")")" = "$PKG" ]'

PYPI_SCRIPT='set -eu
pip install -q build twine setuptools wheel >/dev/null 2>&1
mkdir -p /tmp/p/src && cd /tmp/p
printf "[build-system]\nrequires=[\"setuptools\"]\nbuild-backend=\"setuptools.build_meta\"\n[project]\nname=\"%s\"\nversion=\"%s\"\n" "$PKG" "$VERSION" > pyproject.toml
printf "[tool.setuptools]\npy-modules=[\"%s\"]\n" "$PKG" >> pyproject.toml
echo "VALUE = \"$PKG\"" > $PKG.py
python -m build --wheel --no-isolation >/dev/null 2>&1
TWINE_USERNAME=smoke TWINE_PASSWORD=$OA_TOKEN twine upload --non-interactive \
  --repository-url "$OA_URL/repos/$REPO/legacy/" dist/*.whl >/dev/null
hp="${OA_URL#*://}"; scheme="${OA_URL%%://*}"
pip install -q --index-url "$scheme://smoke:$OA_TOKEN@$hp/repos/$REPO/simple/" "$PKG==$VERSION" >/dev/null
[ "$(python -c "import $PKG; print($PKG.VALUE)")" = "$PKG" ]'

for f in $FORMATS; do
	case "$f" in
	deb) create_repo "smoke-deb-$id" deb; run deb "$DEB_IMAGE" "smoke-deb-$id" "$DEB_SCRIPT" ;;
	rpm) create_repo "smoke-rpm-$id" rpm; run rpm "$RPM_IMAGE" "smoke-rpm-$id" "$RPM_SCRIPT" ;;
	npm) create_repo "smoke-npm-$id" npm; run npm "$NODE_IMAGE" "smoke-npm-$id" "$NPM_SCRIPT" ;;
	pypi) create_repo "smoke-pypi-$id" pypi; run pypi "$PY_IMAGE" "smoke-pypi-$id" "$PYPI_SCRIPT" ;;
	*) die "unknown format: $f" ;;
	esac
done

if [ "${#failed[@]}" -gt 0 ]; then
	die "failed: ${failed[*]}"
fi
echo "smoke-formats: all formats passed ($FORMATS)"
