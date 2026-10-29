#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# npm: npm publish to the repository, then npm install it back.
# Image: NPM_IMAGE (default node:current-alpine, the newest npm).
# Usage: OA_TOKEN=... scripts/smoke/npm.sh [URL]
# shellcheck source=lib.sh
. "$(dirname "$0")/lib.sh"
smoke_init npm node:current-alpine "$@"

smoke_run "$(cat <<'SCRIPT'
set -eu
echo "npm $(npm --version)"
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
[ "$(node -p "require(\"$PKG\")")" = "$PKG" ]
SCRIPT
)"
