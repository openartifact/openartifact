#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# conda: hand-build a noarch .tar.bz2, upload it with the documented POST, then
# conda create installs it back from the channel. Image: CONDA_IMAGE (default
# condaforge/miniforge3:latest, the newest conda).
# Usage: OA_TOKEN=... scripts/smoke/conda.sh [URL]
# shellcheck source=lib.sh
. "$(dirname "$0")/lib.sh"
smoke_init conda condaforge/miniforge3:latest "$@"

smoke_run "$(cat <<'SCRIPT'
set -eu
conda --version
export CONDARC=/tmp/condarc
: > "$CONDARC"
[ "$SMOKE_MODE" = install ] || {
  mkdir -p /tmp/p/info "/tmp/p/share/$PKG" && cd /tmp/p
  echo "$PKG" > "share/$PKG/marker.txt"
  echo "share/$PKG/marker.txt" > info/files
  printf '{"name":"%s","version":"%s","build":"0","build_number":0,"depends":[],"noarch":"generic","subdir":"noarch","timestamp":1700000000000,"license":"MIT"}\n' \
    "$PKG" "$VERSION" > info/index.json
  tar -cjf "/tmp/$PKG-$VERSION-0.tar.bz2" info share
  python - <<PY
import base64, os, urllib.request
f = "/tmp/$PKG-$VERSION-0.tar.bz2"
req = urllib.request.Request(os.environ["OA_URL"] + "/repos/" + os.environ["REPO"] + "/noarch",
    data=open(f, "rb").read(), method="POST",
    headers={"X-Filename": os.path.basename(f),
             "Authorization": "Basic " + base64.b64encode(("smoke:" + os.environ["OA_TOKEN"]).encode()).decode()})
print("upload HTTP", urllib.request.urlopen(req).status)
PY
}
[ "$SMOKE_MODE" = publish ] || {
  # fresh conda state: its own package cache and a new environment
  export CONDA_PKGS_DIRS=/tmp/conda-pkgs-install
  channel="${OA_URL%%://*}://smoke:$OA_TOKEN@${OA_URL#*://}/repos/$REPO"
  conda create -y -q -p /tmp/env --override-channels -c "$channel" "$PKG=$VERSION" >/dev/null
  [ "$(cat "/tmp/env/share/$PKG/marker.txt")" = "$PKG" ]
}
SCRIPT
)"
