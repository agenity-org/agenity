# chepherd — multi-stage container build
#
# Stage 1: build Astro web UI
# Stage 2: build Go binary
# Stage 3: minimal runtime image (ubuntu 22.04 + podman for ContainerRuntime)
#
# Usage:
#   podman build -t chepherd:latest .
#   docker build -t chepherd:latest .

# ─── Stage 1: Astro / Svelte web build ────────────────────────────────────────
FROM node:20-alpine AS web-builder
WORKDIR /build/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --prefer-offline
COPY web/ ./
# Build with production API path (no proxy prefix needed — same-origin)
RUN npm run build

# ─── Stage 2: Go binary ───────────────────────────────────────────────────────
FROM golang:1.24-alpine AS go-builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILDDATE=unknown
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-s -w \
      -X 'github.com/chepherd/chepherd/cmd.Version=${VERSION}' \
      -X 'github.com/chepherd/chepherd/cmd.Commit=${COMMIT}' \
      -X 'github.com/chepherd/chepherd/cmd.BuildDate=${BUILDDATE}'" \
    -o /usr/local/bin/chepherd .

# ─── Stage 3: runtime image ───────────────────────────────────────────────────
FROM ubuntu:22.04

ARG DEBIAN_FRONTEND=noninteractive

# Base tooling: git + curl (needed by agents); podman for ContainerRuntime.
# uidmap provides newuidmap/newgidmap required for rootless podman-in-podman.
# gosu lets the entrypoint drop privileges after image-load (runs as root
# briefly to chown the named-volume state dir + load the agent image).
RUN apt-get update && apt-get install -y --no-install-recommends \
    git curl ca-certificates gosu \
    podman fuse-overlayfs slirp4netns uidmap \
    && rm -rf /var/lib/apt/lists/*

# Non-root user for the chepherd process itself
RUN useradd -m -u 1000 -s /bin/bash chepherd \
    && mkdir -p /app/web /home/chepherd/.local/state/chepherd /home/chepherd/.local/share \
    && chown -R chepherd:chepherd /home/chepherd/.local \
    # subuid/subgid ranges required for rootless podman user-namespace mapping.
    && echo "chepherd:100000:65536" >> /etc/subuid \
    && echo "chepherd:100000:65536" >> /etc/subgid

COPY --from=go-builder  /usr/local/bin/chepherd /usr/local/bin/chepherd
COPY --from=web-builder /build/web/dist          /app/web/dist
COPY --from=go-builder  /build/catalog            /app/catalog
COPY scripts/chepherd-entrypoint.sh             /usr/local/bin/chepherd-entrypoint
RUN chmod +x /usr/local/bin/chepherd-entrypoint

# Podman storage config for rootless inside container
RUN mkdir -p /home/chepherd/.config/containers && \
    printf '[storage]\ndriver = "overlay"\n[storage.options.overlay]\nmount_program = "/usr/bin/fuse-overlayfs"\n' \
      > /home/chepherd/.config/containers/storage.conf && \
    chown -R chepherd:chepherd /home/chepherd/.config

# Run as root inside the container (outer podman uses --privileged, so UID 0
# in container = host UID — rootless but with full capabilities for inner podman).
ENV HOME=/home/chepherd
WORKDIR /home/chepherd

# State dir mounted by compose.yaml for persistence across container restarts
VOLUME ["/home/chepherd/.local/state/chepherd"]

EXPOSE 8080 9090

# Run as root inside the container — chepherd-entrypoint chowns the state
# volume, loads the agent image into the in-pod podman storage, then
# `exec gosu chepherd:chepherd ...` drops privileges before the daemon starts.
ENTRYPOINT ["/usr/local/bin/chepherd-entrypoint"]
