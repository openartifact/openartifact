# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later

# ClusterFuzzLite build image. Pinned by digest; Dependabot (docker ecosystem,
# directory /.clusterfuzzlite) bumps it. It ships the Go toolchain the fuzz
# targets are built with (go.mod needs 1.27.x).
FROM gcr.io/oss-fuzz-base/base-builder-go:v1@sha256:1dbadefbb28c6bfd796c64d9c2115977deb8f19494533cc5a3b724c3441df23d

COPY . $SRC/openartifact
WORKDIR $SRC/openartifact
COPY .clusterfuzzlite/build.sh $SRC/
