# syntax=docker/dockerfile:1

# =============================================================================
# Firefly AI Gateway — multi-stage container build
#
# Stage 1 (frontend): build the React/Vite SPA with Bun -> frontend/dist
# Stage 2 (backend):  compile the Go binary, embedding frontend/dist via go:embed
# Stage 3 (runtime):  minimal glibc (Debian slim) image running as non-root
#
# The Turso embedded engine (tursogo) extracts a *glibc*-linked native library
# at runtime, so the runtime image must be glibc-based (Debian/Ubuntu), NOT
# musl-based (Alpine). Building without the `musl` tag embeds the glibc library.
# =============================================================================

# -----------------------------------------------------------------------------
# Stage 1: Frontend production bundle (Bun + Vite)
# -----------------------------------------------------------------------------
FROM oven/bun:1 AS frontend
WORKDIR /app/frontend

# Install dependencies first (cached unless manifests change)
COPY frontend/package.json frontend/bun.lock ./
RUN bun install --frozen-lockfile

# Build the SPA -> produces frontend/dist (index.html + assets/)
COPY frontend/ ./
RUN bun run build \
    && test -f dist/index.html

# -----------------------------------------------------------------------------
# Stage 2: Go binary (embeds the frontend dist via //go:embed all:dist)
# -----------------------------------------------------------------------------
FROM golang:1.26-bookworm AS backend
WORKDIR /src

# Download modules first (cached unless go.mod/go.sum change)
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Copy the full source, then drop in the freshly built frontend assets so the
# go:embed directive in frontend/embed.go has real files to embed.
COPY . .
COPY --from=frontend /app/frontend/dist ./frontend/dist

# Build metadata (override with: --build-arg VERSION=v1.2.3 etc.)
ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

# CGO disabled: the tursogo native library is loaded at runtime via purego,
# not linked at build time, so a static Go binary is sufficient here.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath \
        -ldflags="-s -w -X 'main.Version=${VERSION}' -X 'main.Commit=${COMMIT}' -X 'main.BuildDate=${BUILD_DATE}'" \
        -o /out/firefly ./cmd/firefly

# -----------------------------------------------------------------------------
# Stage 3: Minimal runtime image (glibc-based, non-root)
# -----------------------------------------------------------------------------
FROM debian:bookworm-slim AS runtime

# ca-certificates: outbound HTTPS to upstream AI providers and Turso cloud.
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Non-root service user. /etc/firefly holds seeded config + the Turso replica
# and native-library cache; both are created writable for that user.
RUN useradd --system --create-home --home-dir /home/firefly --shell /usr/sbin/nologin firefly \
    && mkdir -p /etc/firefly \
    && chown -R firefly:firefly /etc/firefly

COPY --from=backend /out/firefly /usr/local/bin/firefly

ENV FIREFLY_CONFIG_DIR=/etc/firefly \
    FIREFLY_ADDR=0.0.0.0:8080

# Persist configuration, the Turso embedded replica, and the native-library
# cache (Firefly defaults TURSO_GO_CACHE_DIR to <config-dir>/data/.turso-cache).
VOLUME ["/etc/firefly"]

EXPOSE 8080
USER firefly

# Default port is 8080 (see -addr / FIREFLY_ADDR). Override flags as needed.
ENTRYPOINT ["/usr/local/bin/firefly"]
CMD ["-config-dir=/etc/firefly", "-addr=0.0.0.0:8080"]
