#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# pub: dart pub publish to a hosted repository, then dart pub get of the
# package back through PUB_HOSTED_URL. Image: PUB_IMAGE (default dart:stable).
# Usage: OA_TOKEN=... scripts/smoke/pub.sh [URL]
# shellcheck source=lib.sh
. "$(dirname "$0")/lib.sh"
smoke_init pub docker.io/library/dart:stable "$@"

smoke_run "$(cat <<'SCRIPT'
set -eu
dart --version
export PUB_CACHE=/tmp/pub-cache
SERVER="$OA_URL/repos/pub/$REPO"
printf '%s\n' "$OA_TOKEN" | dart pub token add "$SERVER"
[ "$SMOKE_MODE" = install ] || {
  mkdir -p /tmp/p/lib && cd /tmp/p
  cat > pubspec.yaml <<EOF
name: $PKG
version: $VERSION
description: Smoke test package published to OpenArtifact.
publish_to: $SERVER
environment:
  sdk: ^3.0.0
EOF
  echo "int marker() => 42;" > lib/$PKG.dart
  echo "# $PKG" > README.md
  echo "## $VERSION" > CHANGELOG.md
  echo "BSD" > LICENSE
  dart pub publish --force
}
[ "$SMOKE_MODE" = publish ] || {
  mkdir -p /tmp/c && cd /tmp/c
  cat > pubspec.yaml <<EOF
name: consumer
publish_to: none
environment:
  sdk: ^3.0.0
dependencies:
  $PKG:
    hosted: $SERVER
    version: $VERSION
EOF
  dart pub get
  [ "$(cat "$PUB_CACHE/hosted/$(echo "$SERVER" | sed -e 's|^[a-z]*://||' -e 's|/|%47|g' -e 's|:|%58|g')/$PKG-$VERSION/lib/$PKG.dart")" = "int marker() => 42;" ]
}
SCRIPT
)"
