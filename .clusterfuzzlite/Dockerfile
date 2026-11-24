# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later

# ClusterFuzzLite build image. Pinned by digest; Dependabot (docker ecosystem,
# directory /.clusterfuzzlite) bumps it. It ships the Go toolchain the fuzz
# targets are built with (go.mod needs 1.27.x).
FROM gcr.io/oss-fuzz-base/base-builder-go:v1@sha256:68eb389f79e1b58277412ef425fab4c412cd4f61c521ce355a49c8cdd1a33e1e

# Modules in a layer of their own: it is rebuilt only when go.mod or go.sum
# change, not on every source change of a local `docker build`.
WORKDIR $SRC/openartifact
COPY go.mod go.sum ./
RUN go mod download

COPY . $SRC/openartifact
COPY .clusterfuzzlite/build.sh $SRC/
