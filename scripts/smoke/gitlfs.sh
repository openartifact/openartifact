#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# gitlfs: git-lfs pushes a 1 MiB binary to a hosted Git LFS repository (lfs.url) with a token,
# a fresh clone pulls it back and the sha256 is compared. The install phase builds the pointer
# from the same deterministic content, so it needs no clone of the publish phase.
# Image: GITLFS_IMAGE (default alpine:latest; git and git-lfs are installed with apk).
# Usage: OA_TOKEN=... scripts/smoke/gitlfs.sh [URL]
# shellcheck source=lib.sh
. "$(dirname "$0")/lib.sh"
smoke_init gitlfs alpine:latest "$@"

smoke_run "$(cat <<'SCRIPT'
set -eu
command -v git-lfs >/dev/null || apk add -q git git-lfs
git lfs version
export HOME=/tmp/home GIT_TERMINAL_PROMPT=0
mkdir -p "$HOME" && cd "$HOME"
git config --global user.name smoke
git config --global user.email smoke@example.com
git config --global init.defaultBranch main
git config --global credential.helper store
git lfs install >/dev/null
lfs="$OA_URL/repos/gitlfs/$REPO"
# the token goes through stdin, never onto a command line
git credential approve <<EOF
url=$lfs
username=smoke
password=$OA_TOKEN

EOF
payload() { yes "marker $PKG $VERSION" | head -c 1048576; }
want="$(payload | sha256sum | cut -d' ' -f1)"
repo() { # repo DIR: a repository whose objects go to the OpenArtifact repository
  git init -q "$1" && cd "$1"
  git config lfs.url "$lfs"
  git lfs track '*.bin' >/dev/null
}
[ "$SMOKE_MODE" = install ] || {
  repo /tmp/pub
  payload > big.bin
  git add .gitattributes big.bin && git commit -qm publish
  git lfs ls-files | grep -q big.bin
  git init -q --bare /tmp/origin.git
  git remote add origin /tmp/origin.git
  git push -q origin main
  [ "$(curl -s -o /dev/null -w '%{http_code}' "$lfs/objects/$want")" = 401 ]
  [ "$(curl -s -o /dev/null -w '%{http_code}' -u "smoke:$OA_TOKEN" "$lfs/objects/$want")" = 200 ]
}
[ "$SMOKE_MODE" = publish ] || {
  # a clone that holds only the pointer, as after `git clone` of a repository that uses LFS
  repo /tmp/get
  payload > src.bin
  git lfs pointer --file src.bin > big.bin
  rm src.bin
  git add .gitattributes big.bin && git commit -qm pointer
  git lfs pull
  [ "$(sha256sum big.bin | cut -d' ' -f1)" = "$want" ]
  git lfs fsck
}
SCRIPT
)"
