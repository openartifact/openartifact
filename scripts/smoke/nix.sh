#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# nix: nix copy --to uploads a store path to the binary cache, then a fresh
# store substitutes it back with the repository key required. Image: NIX_IMAGE
# (default nixos/nix:latest).
# Usage: OA_TOKEN=... scripts/smoke/nix.sh [URL]
# shellcheck source=lib.sh
. "$(dirname "$0")/lib.sh"
smoke_init nix nixos/nix:latest "$@"
SMOKE_SHELL=bash

smoke_run "$(cat <<'SCRIPT'
set -eu
nix --version
host=${OA_URL#*://}; host=${host%%:*}
printf 'machine %s login smoke password %s\n' "$host" "$OA_TOKEN" > /tmp/netrc
export NIX_CONFIG="experimental-features = nix-command flakes
sandbox = false
netrc-file = /tmp/netrc
require-sigs = true"
cache="$OA_URL/repos/$REPO"
expr="derivation {
  name = \"$PKG\"; system = builtins.currentSystem; builder = \"/bin/sh\";
  args = [ \"-c\" \"echo marker-$PKG > \$out\" ];
}"
# the store path is a function of the derivation: both phases know it without a shared store
out=$(nix-store -q --outputs "$(nix-instantiate -E "$expr")")
[ "$SMOKE_MODE" = install ] || {
  [ "$(nix-build --no-out-link -E "$expr")" = "$out" ]
  [ "$(cat "$out")" = "marker-$PKG" ]
  nix copy --to "$cache" "$out"
}
[ "$SMOKE_MODE" = publish ] || {
  key=$(curl -fsS -H "Authorization: Bearer $OA_TOKEN" "$cache/public.key")
  echo "cache key: $key"
  hash=${out#/nix/store/}; hash=${hash%%-*}
  curl -fsS -H "Authorization: Bearer $OA_TOKEN" "$cache/$hash.narinfo" | grep -q "^Sig: ${key%%:*}:"
  # The consumer is another machine: Nix's narinfo cache kept the unsigned upload copy.
  export XDG_CACHE_HOME=/tmp/consumer-cache
  # A fresh store can only get the path from the cache: it has no deriver to build.
  nix copy --from "$cache" --to /tmp/untrusted "$out" --option trusted-public-keys "" 2>&1 | grep -q "lacks a signature by a trusted key" ||
    { echo "path was accepted without the repository key"; exit 1; }
  [ ! -e /tmp/untrusted"$out" ]
  nix copy --from "$cache" --to /tmp/other "$out" --option trusted-public-keys "$key"
  nix path-info --store /tmp/other "$out"
  [ "$(cat /tmp/other"$out")" = "marker-$PKG" ]
}
SCRIPT
)"
