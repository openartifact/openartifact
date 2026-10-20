# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later

# ---- Frontend build stage ---------------------------------------------------
# Builds the Vue 3 SPA so web/dist/ is populated before Go's //go:embed runs.
FROM --platform=$BUILDPLATFORM docker.io/oven/bun:1-alpine AS web-builder

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
FROM --platform=$BUILDPLATFORM docker.io/golang:1.27.3-alpine AS builder

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
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildTime=${BUILD_TIME}" \
    -o openartifact ./cmd/server

# Runtime stage
FROM docker.io/alpine:3.24

WORKDIR /app

# Install runtime dependencies
RUN apk --no-cache add ca-certificates tzdata

# Copy binary from builder
COPY --from=builder /app/openartifact .

# Create data directories (blobs + backups)
RUN mkdir -p /var/lib/artifacts /var/lib/openartifact/backups

# Run as non-root user with a fixed uid/gid so bind mounts are predictable
RUN addgroup -S -g 10001 appgroup && adduser -S -u 10001 -G appgroup appuser && \
    chown -R appuser:appgroup /var/lib/artifacts /var/lib/openartifact /app
VOLUME ["/var/lib/artifacts", "/var/lib/openartifact/backups"]
USER 10001:10001

# Expose port
EXPOSE 8080

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:8080/health || exit 1

# Run the application
CMD ["./openartifact"]
