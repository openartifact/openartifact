#!/usr/bin/env bash
# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# maven: mvn deploy:deploy-file to the repository, then resolve it from a consumer pom (dependency:copy-dependencies, fresh local repo).
# Image: MAVEN_IMAGE (default maven:3-eclipse-temurin-21, the newest Maven 3).
# Usage: OA_TOKEN=... scripts/smoke/maven.sh [URL]
# shellcheck source=lib.sh
. "$(dirname "$0")/lib.sh"
smoke_init maven maven:3-eclipse-temurin-21 "$@"

smoke_run "$(cat <<'SCRIPT'
set -eu
mvn --version | head -n1
url="$OA_URL/repos/maven/$REPO"
cat > /tmp/settings.xml <<EOF
<settings><servers><server><id>oa</id><username>smoke</username><password>$OA_TOKEN</password></server></servers></settings>
EOF
[ "$SMOKE_MODE" = install ] || {
  mkdir -p /tmp/src && cd /tmp/src
  echo "marker $PKG" > marker.txt
  jar cf /tmp/lib.jar marker.txt
  mvn -B -q -s /tmp/settings.xml -Dmaven.repo.local=/tmp/m2p deploy:deploy-file \
    -Dfile=/tmp/lib.jar -DgroupId=com.smoke -DartifactId="$PKG" -Dversion="$VERSION" \
    -Dpackaging=jar -DgeneratePom=true -DrepositoryId=oa -Durl="$url"
}
[ "$SMOKE_MODE" = publish ] || {
  # a consumer with its own, empty local repository
  mkdir -p /tmp/consumer /tmp/out /tmp/x && cat > /tmp/consumer/pom.xml <<EOP
<project xmlns="http://maven.apache.org/POM/4.0.0">
  <modelVersion>4.0.0</modelVersion>
  <groupId>com.smoke</groupId><artifactId>consumer</artifactId><version>1</version>
  <repositories><repository><id>oa</id><url>$url</url></repository></repositories>
  <dependencies><dependency>
    <groupId>com.smoke</groupId><artifactId>$PKG</artifactId><version>$VERSION</version>
  </dependency></dependencies>
</project>
EOP
  mvn -B -q -s /tmp/settings.xml -Dmaven.repo.local=/tmp/m2c -f /tmp/consumer/pom.xml \
    dependency:copy-dependencies -DoutputDirectory=/tmp/out
  cd /tmp/x && jar xf "/tmp/out/$PKG-$VERSION.jar"
  [ "$(cat marker.txt)" = "marker $PKG" ]
}
SCRIPT
)"
