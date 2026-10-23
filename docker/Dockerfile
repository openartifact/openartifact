# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later

# Runtime base: "dhi" (Docker Hardened Images, needs a dhi.io login) or
# "distroless" (anonymous pull). Only the selected stage is built.
ARG RUNTIME=dhi

# Vulnerability scanners to bundle: "none" (default; the image then holds only
# the server), "trivy", "grype" or "all". The binaries are copied from the
# digest-pinned upstream images below; see docs/security/scanning.md.
ARG SCANNERS=none

# ---- Frontend build stage ---------------------------------------------------
# Builds the Vue 3 SPA so web/dist/ is populated before Go's //go:embed runs.
FROM --platform=$BUILDPLATFORM docker.io/oven/bun:1-alpine@sha256:629e17411f1f129dbec3af78d5af9c9f2a937435c80349437206c6b0b7422373 AS web-builder

WORKDIR /web

# Install JS deps with cache-friendly layering
COPY web/package.json web/bun.lock* web/bun.lockb* ./
RUN bun install --frozen-lockfile

# Build the SPA
COPY web/ ./

# Skip vue-tsc type-check (known musl compat issue with TS 6 + project references).
# Vite build catches broken imports; run `vue-tsc --noEmit` locally or in CI.
RUN bunx vite build
# ---- Backend build stage ----------------------------------------------------
FROM --platform=$BUILDPLATFORM docker.io/golang:1.27.3-alpine@sha256:f2c872bcee949b7be9c490efd02b48a8a2fbf947cdf533f1c58525ec9d8170fd AS builder

WORKDIR /app

# Install build dependencies
RUN apk add --no-cache git make

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Replace web/dist with the freshly built assets so //go:embed all:dist
# picks up the real SPA, not the .gitkeep placeholder.
COPY --from=web-builder /web/dist ./web/dist

# Build the application
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown
ARG TARGETOS=linux
ARG TARGETARCH
# The binary reports the 7-character short SHA and an RFC 3339 UTC build time
# (second precision), whatever form the caller passed; the OCI revision/created
# labels below keep the values exactly as given.
RUN COMMIT_SHORT="$(printf '%.7s' "${COMMIT}")"; \
    BUILD_TIME_RFC3339="${BUILD_TIME}"; \
    case "${BUILD_TIME}" in [0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9]*Z) BUILD_TIME_RFC3339="$(printf '%.19sZ' "${BUILD_TIME}")" ;; esac; \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT_SHORT} -X main.buildTime=${BUILD_TIME_RFC3339}" \
    -o openartifact ./cmd/server

# Data directories for the runtime image. The final stage has no shell, so
# they are prepared here and copied in with ownership. The scanner cache holds
# the Trivy / Grype vulnerability databases (OPENARTIFACT_SCANNER_CACHE_DIR);
# /rootfs/scanners stays empty and backs SCANNERS=none.
RUN mkdir -p /rootfs/var/lib/artifacts /rootfs/var/lib/openartifact/backups \
    /rootfs/var/cache/openartifact/scanners /rootfs/scanners

# ---- Scanner binaries (static, digest-pinned) -------------------------------
# Selected by the SCANNERS build argument; only the chosen stage is used.
FROM docker.io/aquasec/trivy:0.76.1@sha256:790fd66d1697395d8693d1acaa1ea702406a20f109b6fc7bac8db623eca6c06c AS trivy-src
FROM docker.io/anchore/grype:v0.121.0@sha256:621d56d1d68106b787022c81546f7e0a36202a25d1239f0c20195fab8c16f7ea AS grype-src

FROM scratch AS scanners-none
COPY --from=builder /rootfs/scanners/ /scanners/

FROM scratch AS scanners-trivy
COPY --from=trivy-src /usr/local/bin/trivy /scanners/trivy

FROM scratch AS scanners-grype
COPY --from=grype-src /grype /scanners/grype

FROM scratch AS scanners-all
COPY --from=trivy-src /usr/local/bin/trivy /scanners/trivy
COPY --from=grype-src /grype /scanners/grype

FROM scanners-${SCANNERS} AS scanners

# ---- Runtime bases (shell-less, digest-pinned) ------------------------------
FROM dhi.io/static:20250419-debian13@sha256:620157c61ee0377151d3e11a7dd4d2ec6da34488f8c63a2706916f88f5f814fd AS runtime-dhi
FROM gcr.io/distroless/static-debian13:nonroot@sha256:d62067879de890a1def9da0e7766b7d20810a5e5555cef8506b752437d7a3e88 AS runtime-distroless

# ---- Runtime stage ----------------------------------------------------------
FROM runtime-${RUNTIME}

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown
LABEL org.opencontainers.image.title="OpenArtifact" \
      org.opencontainers.image.description="Self-hosted artifact repository for 16 package formats: Maven, npm, PyPI, Docker/OCI, Helm, Debian, RPM, Go, Cargo, NuGet, RubyGems, Conda, Conan, Ivy, sbt and raw." \
      org.opencontainers.image.vendor="OpenArtifact" \
      org.opencontainers.image.authors="The OpenArtifact Authors" \
      org.opencontainers.image.licenses="AGPL-3.0-or-later" \
      org.opencontainers.image.url="https://github.com/openartifact/openartifact" \
      org.opencontainers.image.source="https://github.com/openartifact/openartifact" \
      org.opencontainers.image.documentation="https://github.com/openartifact/openartifact#readme" \
      org.opencontainers.image.base.name="dhi.io/static:20250419-debian13" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${BUILD_TIME}"

WORKDIR /app

COPY --from=builder --chown=10001:10001 /app/openartifact /app/openartifact

# Data directories (blobs + backups), owned by the fixed runtime uid/gid so
# bind mounts and the chart's fsGroup stay predictable.
COPY --from=builder --chown=10001:10001 /rootfs/var/lib/ /var/lib/

# Vulnerability scanner database cache, writable by the runtime uid even on a
# read-only root filesystem when mounted as a volume.
COPY --from=builder --chown=10001:10001 /rootfs/var/cache/ /var/cache/
ENV OPENARTIFACT_SCANNER_CACHE_DIR=/var/cache/openartifact/scanners

# Trivy / Grype when built with SCANNERS=trivy|grype|all (empty otherwise).
# PATH is set explicitly because the server finds them by command name.
COPY --from=scanners /scanners/ /usr/local/bin/
ENV PATH=/usr/local/bin:/usr/bin:/bin

VOLUME ["/var/lib/artifacts", "/var/lib/openartifact/backups", "/var/cache/openartifact/scanners"]
USER 10001:10001

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD ["/app/openartifact", "-healthcheck"]

# Run the application
CMD ["/app/openartifact"]
