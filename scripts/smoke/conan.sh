#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# conan: conan create + conan upload of a tiny recipe (and its binary package) to the
# repository, then conan install of it into a fresh Conan home and compare the content.
# Image: CONAN_IMAGE (default python:3-slim; Conan 2 is installed with pip).
# Usage: OA_TOKEN=... scripts/smoke/conan.sh [URL]
# shellcheck source=lib.sh
. "$(dirname "$0")/lib.sh"
smoke_init conan python:3-slim "$@"

smoke_run "$(cat <<'SCRIPT'
set -eu
pip install -q conan >/dev/null 2>&1
conan --version
# conan_home NAME: a Conan home of its own (cache, profile, remotes) with the repository as remote "smoke".
conan_home() {
  export CONAN_HOME=/tmp/conan-home-$1
  conan profile detect --force >/dev/null 2>&1
  conan remote add smoke "$OA_URL/repos/$REPO" >/dev/null
  conan remote login smoke smoke -p "$OA_TOKEN" >/dev/null
}
[ "$SMOKE_MODE" = install ] || {
  conan_home publish
  mkdir -p /tmp/p && cd /tmp/p
  cat > conanfile.py <<EOP
import os
from conan import ConanFile
from conan.tools.files import save


class SmokeConan(ConanFile):
    description = "OpenArtifact smoke test package"

    def package(self):
        save(self, os.path.join(self.package_folder, "share", "marker.txt"), "$PKG")
EOP
  conan create . --name="$PKG" --version="$VERSION" >/dev/null
  conan upload "$PKG/$VERSION" -r smoke --confirm
}
[ "$SMOKE_MODE" = publish ] || {
  # a fresh Conan home: nothing in the local cache, the package can only come from the remote
  conan_home install
  mkdir -p /tmp/c && cd /tmp/c
  conan install --requires="$PKG/$VERSION" -r smoke --deployer=direct_deploy --deployer-folder=/tmp/c/out
  [ "$(cat "/tmp/c/out/direct_deploy/$PKG/share/marker.txt")" = "$PKG" ]
}
SCRIPT
)"
