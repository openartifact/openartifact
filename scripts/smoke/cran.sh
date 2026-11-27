#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# cran: R CMD build of a tiny package + curl upload to the repository, then
# install.packages() it back through PACKAGES. Image: CRAN_IMAGE (default r-base:latest).
# Usage: OA_TOKEN=... scripts/smoke/cran.sh [URL]
# shellcheck source=lib.sh
. "$(dirname "$0")/lib.sh"
smoke_init cran docker.io/library/r-base:latest "$@"

smoke_run "$(cat <<'SCRIPT'
set -eu
R --version | head -1
base="$OA_URL/repos/cran/$REPO"
hp="${OA_URL#*://}"; scheme="${OA_URL%%://*}"
[ "$SMOKE_MODE" = install ] || {
  mkdir -p /tmp/p/"$PKG"/R && cd /tmp/p
  cat > "$PKG/DESCRIPTION" <<EOF
Package: $PKG
Type: Package
Title: Smoke Test Package
Version: $VERSION
Description: A tiny package for the smoke test.
License: MIT + file LICENSE
Authors@R: person("Smoke", "Test", email = "smoke@example.com", role = c("aut", "cre"))
Encoding: UTF-8
EOF
  printf 'YEAR: 2026\nCOPYRIGHT HOLDER: smoke\n' > "$PKG/LICENSE"
  echo 'value <- function() "'"$PKG"'"' > "$PKG/R/value.R"
  echo 'export(value)' > "$PKG/NAMESPACE"
  R CMD build "$PKG" >/dev/null
  curl -fsS -u "smoke:$OA_TOKEN" -T "${PKG}_$VERSION.tar.gz" "$base/src/contrib/${PKG}_$VERSION.tar.gz" >/dev/null
}
[ "$SMOKE_MODE" = publish ] || {
  mkdir -p /tmp/lib
  # credentials in the repository URL: R downloads with libcurl, which sends them as Basic auth
  repo_url="$scheme://smoke:$OA_TOKEN@$hp/repos/cran/$REPO"
  Rscript -e 'install.packages(commandArgs(TRUE)[1], lib = "/tmp/lib", repos = c(oa = commandArgs(TRUE)[2]), type = "source")' "$PKG" "$repo_url"
  Rscript -e 'library(commandArgs(TRUE)[1], lib.loc = "/tmp/lib", character.only = TRUE); stopifnot(do.call(value, list()) == commandArgs(TRUE)[1])' "$PKG"
}
SCRIPT
)"
