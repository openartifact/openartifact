#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# huggingface: a proxy repository of the Hugging Face Hub (no hosted repositories
# exist for this format). `hf download` fetches one small file through it, then
# downloads it again into a fresh cache. The publish phase is the first download
# (it fills the proxy cache), the install phase the second one.
# Image: HUGGINGFACE_IMAGE (default python:3.14-slim; the newest huggingface_hub is pip installed).
# Needs Internet access for the server and the container (huggingface.co).
# Usage: OA_TOKEN=... scripts/smoke/huggingface.sh [URL]
# shellcheck source=lib.sh
. "$(dirname "$0")/lib.sh"
SMOKE_REPO_BODY='{"repo_type":"proxy"}'
smoke_init huggingface docker.io/library/python:3.14-slim "$@"

smoke_run "$(cat <<'SCRIPT'
set -eu
pip install -q huggingface_hub 2>&1 | tail -3
hf version
export HF_ENDPOINT="$OA_URL/repos/$REPO"
export HF_TOKEN="$OA_TOKEN"
export HF_HUB_DOWNLOAD_TIMEOUT=600
model=hf-internal-testing/tiny-random-bert
fetch() { # fetch DIR: a download into a fresh cache and local directory
  HF_HOME="$1/home" hf download "$model" config.json --local-dir "$1/out" >/dev/null
  grep -q '"model_type": "bert"' "$1/out/config.json"
  sha256sum "$1/out/config.json" | cut -d' ' -f1
}
[ "$SMOKE_MODE" = install ] || { first=$(fetch /tmp/first); echo "$first" > /tmp/first.sha; }
[ "$SMOKE_MODE" = publish ] || {
  second=$(fetch /tmp/second)
  # the same file again, now served from the proxy cache
  [ "$second" = "$(fetch /tmp/third)" ]
  [ "$SMOKE_MODE" = install ] || [ "$second" = "$(cat /tmp/first.sha)" ]
}
SCRIPT
)"
