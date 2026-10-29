#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# composer: composer archive builds the dist zip, curl PUTs it to the upload
# route, composer require installs it back. Image: COMPOSER_IMAGE (default
# composer:latest, the newest composer).
# Usage: OA_TOKEN=... scripts/smoke/composer.sh [URL]
# shellcheck source=lib.sh
. "$(dirname "$0")/lib.sh"
smoke_init composer composer:latest "$@"

smoke_run "$(cat <<'SCRIPT'
set -eu
composer --version
export COMPOSER_ALLOW_SUPERUSER=1 COMPOSER_HOME=/tmp/composer-home
hp="${OA_URL#*://}"
[ "$SMOKE_MODE" = install ] || {
  mkdir -p /tmp/p && cd /tmp/p
  printf '{"name":"smoke/%s","description":"smoke","version":"%s","license":"MIT","autoload":{"files":["marker.php"]}}\n' "$PKG" "$VERSION" > composer.json
  printf '<?php\nfunction smoke_marker() { return "%s"; }\n' "$PKG" > marker.php
  composer archive --format=zip --dir=/tmp --file=pkg
  curl -fsS -X PUT -H "Authorization: Bearer $OA_TOKEN" --data-binary @/tmp/pkg.zip \
    "$OA_URL/repos/$REPO/packages/upload/smoke/$PKG/$VERSION" >/dev/null
}
[ "$SMOKE_MODE" = publish ] || {
  # fresh composer state: an empty cache and an empty project
  export COMPOSER_CACHE_DIR=/tmp/composer-cache-install
  mkdir -p /tmp/c && cd /tmp/c
  printf '{"name":"smoke/consumer","repositories":[{"type":"composer","url":"%s/repos/%s"},{"packagist.org":false}],"config":{"secure-http":false}}\n' "$OA_URL" "$REPO" > composer.json
  printf '{"http-basic":{"%s":{"username":"token","password":"%s"}}}\n' "${hp%%/*}" "$OA_TOKEN" > auth.json
  composer require --no-interaction -q "smoke/$PKG:$VERSION"
  [ "$(php -r 'require "vendor/autoload.php"; echo smoke_marker();')" = "$PKG" ]
}
SCRIPT
)"
