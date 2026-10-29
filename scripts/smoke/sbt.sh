#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# sbt: sbt publish of a tiny jar (Maven layout) to the sbt repository, then dependency
# resolution of it by sbt in a fresh project with fresh caches, and a comparison of the jar content.
# Image: SBT_IMAGE (default: a pinned sbtscala/scala-sbt tag, sbt 1.13.1 on Temurin 21).
# Usage: OA_TOKEN=... scripts/smoke/sbt.sh [URL]
# shellcheck source=lib.sh
. "$(dirname "$0")/lib.sh"
smoke_init sbt sbtscala/scala-sbt:eclipse-temurin-21.0.12.1_1_1.13.1_2.13.18 "$@"

smoke_run "$(cat <<'SCRIPT'
set -eu
sbt --version 2>/dev/null | tail -n 2
# sbt matches credentials on the host alone, under the realm of the server's challenge
host="${OA_URL#*://}"; host="${host%%[:/]*}"
url="$OA_URL/repos/sbt/$REPO"
export OA_HOST="$host" OA_REPO_URL="$url"
sbt_run() { sbt -batch -Dsbt.color=false -Dsbt.supershell=false -Dsbt.server.forcestart=false "$@"; }
[ "$SMOKE_MODE" = install ] || {
  mkdir -p /tmp/p/src/main/resources /tmp/p/project && cd /tmp/p
  echo "marker $PKG" > src/main/resources/marker.txt
  cat > build.sbt <<EOB
organization := "com.smoke"
name := "$PKG"
version := "$VERSION"
crossPaths := false
autoScalaLibrary := false
publishTo := Some("oa" at sys.env("OA_REPO_URL"))
credentials += Credentials("openartifact", sys.env("OA_HOST"), "smoke", sys.env("OA_TOKEN"))
EOB
  sbt_run publish
}
[ "$SMOKE_MODE" = publish ] || {
  # a fresh project with fresh caches: the module can only come from the repository
  export COURSIER_CACHE=/tmp/coursier-cache-install
  mkdir -p /tmp/c/project && cd /tmp/c
  cat > build.sbt <<EOB
scalaVersion := "2.13.18"
autoScalaLibrary := false
resolvers += "oa" at sys.env("OA_REPO_URL")
credentials += Credentials("openartifact", sys.env("OA_HOST"), "smoke", sys.env("OA_TOKEN"))
libraryDependencies += "com.smoke" % "$PKG" % "$VERSION"
TaskKey[Unit]("check") := {
  val jar = (Compile / dependencyClasspath).value.map(_.data).find(_.getName == "$PKG-$VERSION.jar")
    .getOrElse(sys.error("the module was not resolved"))
  val got = IO.withTemporaryDirectory { d => IO.unzip(jar, d); IO.read(d / "marker.txt").trim }
  if (got != "marker $PKG") sys.error("wrong content: " + got)
}
EOB
  sbt_run -Dsbt.ivy.home=/tmp/ivy-install check
}
SCRIPT
)"
