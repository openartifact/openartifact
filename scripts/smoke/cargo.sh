#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# cargo: cargo publish to the sparse registry, then a second crate depends on it
# (registry = "smoke") and cargo run prints the marker. Image: CARGO_IMAGE
# (default rust:latest, the newest cargo).
# Usage: OA_TOKEN=... scripts/smoke/cargo.sh [URL]
# shellcheck source=lib.sh
. "$(dirname "$0")/lib.sh"
smoke_init cargo rust:latest "$@"

smoke_run "$(cat <<'SCRIPT'
set -eu
cargo --version
export CARGO_REGISTRIES_SMOKE_INDEX="sparse+$OA_URL/repos/$REPO/index/"
export CARGO_REGISTRIES_SMOKE_TOKEN="Bearer $OA_TOKEN"
export CARGO_REGISTRIES_SMOKE_CREDENTIAL_PROVIDER=cargo:token
[ "$SMOKE_MODE" = install ] || {
  export CARGO_HOME=/tmp/cargo-home CARGO_TARGET_DIR=/tmp/target
  mkdir -p "$CARGO_HOME" /tmp/p/src
  cd /tmp/p
  cat > Cargo.toml <<EOF
[package]
name = "$PKG"
version = "$VERSION"
edition = "2021"
description = "OpenArtifact smoke test crate"
license = "MIT"
publish = ["smoke"]
EOF
  echo "pub fn marker() -> &'static str { \"$PKG\" }" > src/lib.rs
  cargo publish --registry smoke
}
[ "$SMOKE_MODE" = publish ] || {
  # fresh cargo state: its own home (index cache, downloaded crates) and target directory
  export CARGO_HOME=/tmp/cargo-home-install CARGO_TARGET_DIR=/tmp/target-install
  mkdir -p "$CARGO_HOME" /tmp/c/src
  cd /tmp/c
  cat > Cargo.toml <<EOF
[package]
name = "consumer"
version = "0.1.0"
edition = "2021"

[dependencies]
$PKG = { version = "=$VERSION", registry = "smoke" }
EOF
  echo "fn main() { println!(\"{}\", $PKG::marker()); }" > src/main.rs
  [ "$(cargo run -q)" = "$PKG" ]
}
SCRIPT
)"
