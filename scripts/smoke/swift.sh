#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# swift: a tiny library is archived with `swift package archive-source` and
# PUT to a public Swift Package Registry repository, then a second package
# depends on it with .package(id:) and swift package resolve / swift build fetch
# it back. Image: SWIFT_IMAGE (default swift:latest, the newest toolchain).
# SwiftPM never sends credentials over plain http (not even from ~/.netrc, and
# `login` refuses the URL), so the consumed repository is a public one and the
# publish is the registry's own multipart PUT with curl and the token.
# Usage: OA_TOKEN=... scripts/smoke/swift.sh [URL]
# shellcheck source=lib.sh
. "$(dirname "$0")/lib.sh"
SMOKE_REPO_BODY='{"public":true}'
smoke_init swift swift:latest "$@"

smoke_run "$(cat <<'SCRIPT'
set -eu
swift --version 2>&1 | head -1
command -v zip >/dev/null && command -v curl >/dev/null || { apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq zip curl >/dev/null; }
# the scope and the name must match the Swift grammar: letters, digits and single dashes
SCOPE=smoke
NAME="$(printf '%s' "$PKG" | tr -c 'A-Za-z0-9-' '-')"
ID="$SCOPE.$NAME"
URL="$OA_URL/repos/swift/$REPO"
export HOME=/tmp/swift-home
mkdir -p "$HOME"
[ "$SMOKE_MODE" = install ] || {
  mkdir -p /tmp/p && cd /tmp/p
  swift package init --type library --name SmokeLib >/dev/null
  git init -q . && git add -A && git -c user.email=smoke@example.com -c user.name=smoke commit -qm smoke
  swift package archive-source -o /tmp/SmokeLib.zip
  cat > /tmp/package-metadata.json <<EOF
{"author": {"name": "smoke"}, "description": "OpenArtifact smoke test package $PKG"}
EOF
  code="$(curl -sS -o /tmp/publish.out -w '%{http_code}' -X PUT -H "Authorization: Bearer $OA_TOKEN" \
    -H 'Accept: application/vnd.swift.registry.v1+json' \
    -F 'source-archive=@/tmp/SmokeLib.zip;type=application/zip' \
    -F 'metadata=@/tmp/package-metadata.json;type=application/json' "$URL/$SCOPE/$NAME/$VERSION")"
  [ "$code" = 201 ] || { echo "publish answered $code: $(cat /tmp/publish.out)" >&2; exit 1; }
}
[ "$SMOKE_MODE" = publish ] || {
  mkdir -p /tmp/c/Sources/consumer && cd /tmp/c
  cat > Package.swift <<EOF
// swift-tools-version:5.9
import PackageDescription
let package = Package(
  name: "consumer",
  dependencies: [.package(id: "$ID", from: "$VERSION")],
  targets: [.executableTarget(name: "consumer", dependencies: [.product(name: "SmokeLib", package: "$ID")])]
)
EOF
  echo 'import SmokeLib' > Sources/consumer/main.swift
  swift package-registry set "$URL" --allow-insecure-http
  swift package resolve
  grep -q "\"version\" : \"$VERSION\"" Package.resolved
  swift build
}
SCRIPT
)"
