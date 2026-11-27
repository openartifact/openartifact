#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# p2: a proxy repository of the Eclipse release update site (the format has no
# hosted repositories). A Tycho eclipse-plugin that requires org.eclipse.equinox.common
# is built against it: Tycho reads the composite metadata, follows the children the
# proxy relocated (including the `_host/` one outside the repository), and downloads
# the bundle. The publish phase is the build that fills the proxy cache, the install
# phase a second build with an empty local Maven repository and p2 agent cache.
# Image: P2_IMAGE (default maven:3-eclipse-temurin-21; Tycho 4 supports its Maven 3.9).
# Needs Internet access for the server and the container (download.eclipse.org, Maven Central).
# Usage: OA_TOKEN=... scripts/smoke/p2.sh [URL]
# shellcheck source=lib.sh
. "$(dirname "$0")/lib.sh"
SMOKE_REPO_BODY='{"repo_type":"proxy","upstream_url":"https://download.eclipse.org/releases/2024-12"}'
smoke_init p2 "${P2_IMAGE:-maven:3-eclipse-temurin-21}" "$@"

smoke_run "$(cat <<'SCRIPT'
set -eu
mvn --version | head -n1
url="$OA_URL/repos/p2/$REPO/"
cat > /tmp/settings.xml <<EOF
<settings><servers><server><id>oa</id><username>smoke</username><password>$OA_TOKEN</password></server></servers></settings>
EOF
build() { # build DIR: a Tycho build with its own, empty local repository
  rm -rf "$1" && mkdir -p "$1/src/com/smoke" "$1/META-INF" && cd "$1"
  cat > pom.xml <<EOP
<project xmlns="http://maven.apache.org/POM/4.0.0">
  <modelVersion>4.0.0</modelVersion>
  <groupId>com.smoke</groupId><artifactId>com.smoke.consumer</artifactId><version>1.0.0-SNAPSHOT</version>
  <packaging>eclipse-plugin</packaging>
  <properties><tycho.version>4.0.10</tycho.version></properties>
  <repositories><repository><id>oa</id><layout>p2</layout><url>$url</url></repository></repositories>
  <build><plugins><plugin>
    <groupId>org.eclipse.tycho</groupId><artifactId>tycho-maven-plugin</artifactId>
    <version>\${tycho.version}</version><extensions>true</extensions>
  </plugin></plugins></build>
</project>
EOP
  cat > META-INF/MANIFEST.MF <<'EOM'
Manifest-Version: 1.0
Bundle-ManifestVersion: 2
Bundle-Name: Consumer
Bundle-SymbolicName: com.smoke.consumer
Bundle-Version: 1.0.0.qualifier
Bundle-RequiredExecutionEnvironment: JavaSE-17
Require-Bundle: org.eclipse.equinox.common
EOM
  printf 'source.. = src/\nbin.includes = META-INF/,.\n' > build.properties
  cat > src/com/smoke/Hello.java <<'EOJ'
package com.smoke;

import org.eclipse.core.runtime.Path;

public class Hello {
    public static String name() { return new Path("/a/b/c").lastSegment(); }
}
EOJ
  mvn -B -q -s /tmp/settings.xml -Dmaven.repo.local="$1/m2" package
  # compiled against org.eclipse.core.runtime.Path, a class of the bundle the proxy served
  unzip -l target/com.smoke.consumer-1.0.0-SNAPSHOT.jar | grep -q 'com/smoke/Hello.class'
}
[ "$SMOKE_MODE" = install ] || build /tmp/first
[ "$SMOKE_MODE" = publish ] || {
  build /tmp/second
  # the metadata is served by the proxy, with the composite children relocated
  curl -fsS -u "smoke:$OA_TOKEN" -o /tmp/composite.jar "${url}compositeContent.jar"
  unzip -p /tmp/composite.jar compositeContent.xml | grep -q "_host/technology/epp/"
  ! unzip -p /tmp/composite.jar compositeContent.xml | grep -q '\.\./\.\./technology'
}
SCRIPT
)"
