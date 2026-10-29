#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# rubygems: gem build + gem push to the repository, then gem install it back
# through the compact index. Image: RUBYGEMS_IMAGE (default ruby:latest).
# Usage: OA_TOKEN=... scripts/smoke/rubygems.sh [URL]
# shellcheck source=lib.sh
. "$(dirname "$0")/lib.sh"
smoke_init rubygems docker.io/library/ruby:latest "$@"

smoke_run "$(cat <<'SCRIPT'
set -eu
echo "gem $(gem --version)"
mkdir -p /tmp/p/lib && cd /tmp/p
echo "module Smoke; VALUE = \"$PKG\"; end" > "lib/$PKG.rb"
cat > "$PKG.gemspec" <<EOF
Gem::Specification.new do |s|
  s.name = "$PKG"
  s.version = "$VERSION"
  s.summary = "smoke test gem"
  s.authors = ["smoke"]
  s.license = "MIT"
  s.files = ["lib/$PKG.rb"]
end
EOF
gem build "$PKG.gemspec" >/dev/null
GEM_HOST_API_KEY="Bearer $OA_TOKEN" gem push "$PKG-$VERSION.gem" --host "$OA_URL/repos/$REPO"
hp="${OA_URL#*://}"; scheme="${OA_URL%%://*}"
gem install "$PKG" --version "$VERSION" --clear-sources --no-document --install-dir /tmp/g \
  --source "$scheme://smoke:$OA_TOKEN@$hp/repos/$REPO/" >/dev/null
GEM_PATH=/tmp/g ruby -e "require \"$PKG\"; abort 'wrong content' unless Smoke::VALUE == \"$PKG\"; puts Smoke::VALUE"
SCRIPT
)"
