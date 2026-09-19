# syntax=docker/dockerfile:1@sha256:ecfaec9ed6d810b56388c508f4121597bfbba70d41a6dfeee4d8cad5f295fc32
#
# The frontend is digest-pinned like every FROM below it. It was the one
# reference in this file that was not, and it is fetched before any of them —
# so a Docker Hub hiccup resolving it failed the build before a single layer
# was read, which is how `main` went red on an afternoon when nothing about the
# image had changed. A digest is immutable and can be served from any mirror;
# a floating tag has to be resolved from the registry every time.
#
# Dependabot advances the FROM digests below, but its docker ecosystem reads
# FROM lines and this is a comment — so if it is still on this digest a release
# or two from now, bump it by hand:
#   docker buildx imagetools inspect docker/dockerfile:1 --format '{{.Manifest.Digest}}'
#
# Multi-platform, multi-target image.
#
# Two images:
#   overcast       — full image with embedded web management console (default)
#   overcast-slim  — Go binary only, no UI, SQLite excluded (for CI pipelines)
#
# Build for the current platform — prefer the targets, which tag the image after
# the current branch so parallel worktrees cannot build into one name:
#   make docker-console        # console (default), overcast:<sanitised branch>
#   make docker-slim           # slim, overcast-slim:<sanitised branch>
#   make docker-clean          # remove this branch's pair when you are done
#
# By hand, if you must:
#   docker build -t overcast:dev .                          # console (default)
#   docker build --target slim -t overcast-slim:dev .       # slim
#
# Build for all supported platforms (requires docker buildx):
#   docker buildx build --platform linux/amd64,linux/arm64 -t ghcr.io/overcast-sh/overcast:latest --push .
#   docker buildx build --platform linux/amd64,linux/arm64 --target slim -t ghcr.io/overcast-sh/overcast-slim:latest --push .
#
# `--target slim` is the whole instruction: the target selects a builder stage
# that compiles `-tags slim,nosqlite`, so there is no second thing to remember.
# There used to be — a NOSQLITE build arg that a single builder stage branched
# on — and every caller that passed `--target slim` and forgot it silently got
# the full binary in the slim image. The release workflow was one of them, so
# `overcast-slim` shipped with the SPA, SQLite and /_overcast/mcp for several releases
# (#798). A build arg that must agree with the target is a build arg that will
# eventually disagree with it; the flavour is now a property of the target
# alone, and each builder asserts what it produced (see the RUN steps below).
#
# Why this works cross-platform:
#   - CGO_ENABLED=0: pure-Go SQLite (modernc.org/sqlite), no C compiler needed
#   - GOARCH is set automatically by buildx to match the target platform
#   - The golang:alpine builder cross-compiles natively — no QEMU in the build stage
#   - Alpine runtime image has both amd64 and arm64 variants
#
# Base images are pinned by digest, with the tag kept for readability. A tag is
# a moving target: upstream retags alpine:3.20 and the next release ships
# different bytes than the one that was tested, with nothing in the diff to say
# so. The digest makes the image a function of this file, which is what lets
# the build be reproduced and lets CI pull through a registry mirror without
# that changing what ships — a mirror cannot serve different content for a
# digest.
#
# Each digest is the multi-arch index, not a platform manifest, so buildx still
# resolves per-platform underneath. Dependabot keeps them current
# (.github/dependabot.yml); pinning without that would freeze out base-image
# security updates, which is worse than not pinning at all.

# ---- Stage 1: Web UI build --------------------------------------------------
# Builds the SPA (Vite). The compiled assets are embedded into the Go binary
# by the console builder — Node.js is NOT present in any runtime image, and
# the slim build never reaches this stage at all.
FROM --platform=$BUILDPLATFORM node:24-alpine@sha256:e67514e5d0f6c46656005e1b693b2ec9d52e80b641307de684d4a015ba7a4eaf AS web-builder

WORKDIR /web

# corepack resolves the pnpm version pinned in package.json's packageManager
# field. It shipped bundled with Node through v24 but is gone from newer
# distributions (Node 26 LTS included, due October 2026) — install it as its
# own npm package instead, which works the same regardless of what Node
# bundles. See https://github.com/overcast-sh/overcast/issues/558.
ENV COREPACK_ENABLE_DOWNLOAD_PROMPT=0
RUN npm install -g corepack@latest && corepack enable pnpm

# Install dependencies first for layer caching.
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile --ignore-scripts

# Nothing is generated for the SPA: the console fetches its docs navigation
# from the Go BFF at runtime (internal/docsindex), so no Go toolchain and no
# preliminary generation stage is needed here.
COPY web/ .
RUN VITE_BUNDLED=true pnpm run build

# ---- Stage 2: Go sources (shared by both builders) -------------------------
# Everything up to (but not including) the SPA overlay and the compile itself.
# Both builders start here, so the module download and source COPYs are one
# cached layer set rather than two.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:4c9fe60190a2a3350ddc51de80d0224b8a6698d12bdfc999fee45ea9d6c46dbc AS go-src

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY embed.go embed_slim.go ./
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY docs/ ./docs/

# The in-container Lambda init, which internal/services/lambda/initbin embeds
# (`make lambda-init` runs the same two commands). It is built here, in the
# shared stage, so it exists before either binary is compiled: //go:embed reads
# the tree as it is at compile time, and a missing artefact compiles perfectly
# well and then fails at the first invoke that needs it.
#
# Both Linux architectures, whatever this image's own platform is: the init has
# to match the *function's* image, and an `Architectures: [arm64]` function runs
# under emulation on an amd64 host. Nothing at build time can say which half
# will be wanted, so both ship — see internal/services/lambda/initbin.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" \
        -o internal/services/lambda/initbin/dist/lambda-init-linux-amd64 ./cmd/lambda-init \
    && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" \
        -o internal/services/lambda/initbin/dist/lambda-init-linux-arm64 ./cmd/lambda-init

# The aws-sdk shape tables internal/awsshapes embeds (`make aws-sdk-shapes` runs
# the same command): packed here, in the shared stage, from the committed text
# tables, for the same reason as the init above — //go:embed reads the tree at
# compile time.
RUN go run ./cmd/awsshapes-pack

# ---- Stage 3: Go build, slim flavour ---------------------------------------
# No SPA overlay and no dependency on web-builder at all: `-tags slim` compiles
# embed_slim.go, whose WebDistFS is an empty embed.FS. BuildKit therefore skips
# the whole Node stage for a `--target slim` build.
FROM go-src AS go-builder-slim

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev

RUN CGO_ENABLED=0 \
    GOOS=${TARGETOS:-linux} \
    GOARCH=${TARGETARCH:-amd64} \
    go build \
        -trimpath \
        -tags slim,nosqlite \
        -ldflags="-w -s -X main.version=${VERSION}" \
        -o /overcast \
        ./cmd/overcast

# Assert the binary really is slim, by looking at the binary rather than
# trusting the command line that produced it — the failure this guards against
# was precisely a build that looked right and compiled the wrong tags.
#
# Three markers, one per thing `slim,nosqlite` is supposed to remove: the
# embedded SPA's file names (//go:embed all:web/dist, !slim only), the runtime
# MCP route (registered in internal/router/mcp_routes.go, !slim only), and the
# SQLite driver (!nosqlite only). A cross-compiled binary is just bytes to
# grep, so this works for every --platform without QEMU.
RUN for marker in 'web/dist/' '/_overcast/mcp' 'modernc.org/sqlite'; do \
        if grep -q -F "$marker" /overcast; then \
            echo "slim binary contains '$marker' — it was not built with -tags slim,nosqlite" >&2; \
            exit 1; \
        fi; \
    done \
    && echo "slim binary verified: no SPA, no /_overcast/mcp, no SQLite"

# ---- Stage 4: Go build, console flavour ------------------------------------
FROM go-src AS go-builder-console

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev

# Overlay the built SPA so //go:embed all:web/dist can pick it up. The repo's
# committed web/dist/.gitkeep is .dockerignore'd, so this COPY is the only
# thing that populates the directory — assert it brought a real SPA rather
# than letting a UI-less image ship.
COPY --from=web-builder /web/dist /src/web/dist
RUN test -f /src/web/dist/index.html \
    || (echo "web/dist/index.html missing — the SPA build produced no output" >&2; exit 1)

RUN CGO_ENABLED=0 \
    GOOS=${TARGETOS:-linux} \
    GOARCH=${TARGETARCH:-amd64} \
    go build \
        -trimpath \
        -ldflags="-w -s -X main.version=${VERSION}" \
        -o /overcast \
        ./cmd/overcast

# The mirror image of the slim assertion, so a future attempt to make slim
# genuinely slim cannot do it by making console slim too.
RUN for marker in 'web/dist/index.html' '/_overcast/mcp'; do \
        if ! grep -q -F "$marker" /overcast; then \
            echo "console binary is missing '$marker' — a console build must not use -tags slim" >&2; \
            exit 1; \
        fi; \
    done \
    && echo "console binary verified: SPA and /_overcast/mcp present"

# ---- Stage 5: shared runtime base ------------------------------------------
# Both slim and console images share the same OS-level setup.
FROM alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b AS base

# bash is here for exactly one caller: the Java LocalStack Testcontainers
# module (org.testcontainers.localstack.LocalStackContainer) replaces the
# image's entrypoint with `sh -c` and then copies in a /testcontainers_start.sh
# whose shebang is #!/bin/bash. With no real bash the kernel refuses the
# shebang and the container dies with "sh: /testcontainers_start.sh: not found"
# before the emulator starts. busybox cannot stand in — it dispatches on
# argv[0] and has no "bash" applet — so this is a genuine package (~1 MB
# compressed). See #1546 and docs/testcontainers.md. Overcast's own scripts are
# /bin/sh and stay that way.
RUN apk add --no-cache ca-certificates su-exec bash

RUN addgroup -S overcast && adduser -S overcast -G overcast
# /data is where Overcast keeps state. /var/lib/localstack is where
# LocalStack's own published compose file mounts its state volume, and a
# compose file migrated line by line still names it — so the directory exists
# here, owned by the same user, and a named volume mounted there inherits that
# ownership instead of arriving root-owned and unwritable. Overcast reads it
# only when it is genuinely a mount and nothing else says where state goes;
# see adoptLocalStackVolume in internal/config.
RUN mkdir -p /data /var/lib/localstack && chown overcast:overcast /data /var/lib/localstack

COPY docker/entrypoint.sh /usr/local/bin/entrypoint.sh
# Back-compat: the script was previously installed as entrypoint-slim.sh;
# keep the old path working for anyone overriding --entrypoint explicitly.
#
# docker-entrypoint.sh is LocalStack's name for the same thing, and the Java
# Testcontainers module hard-codes it: its generated starter script exports the
# *_DOCKER_FLAGS labels and then execs /usr/local/bin/docker-entrypoint.sh. The
# alias is the same kind of compatibility surface as /etc/localstack/init and
# /var/lib/localstack — LocalStack's path, answered by Overcast's own script.
RUN ln -s entrypoint.sh /usr/local/bin/entrypoint-slim.sh \
    && ln -s entrypoint.sh /usr/local/bin/docker-entrypoint.sh
COPY docker/awslocal /usr/local/bin/awslocal

# Init hook directories (LocalStack-compatible + Overcast-native).
RUN mkdir -p /etc/localstack/init/boot.d \
             /etc/localstack/init/start.d \
             /etc/localstack/init/ready.d \
             /etc/localstack/init/shutdown.d \
             /etc/overcast/init/boot.d \
             /etc/overcast/init/start.d \
             /etc/overcast/init/ready.d \
             /etc/overcast/init/shutdown.d

# The image bakes exactly these two variables and deliberately nothing else.
# A value baked as ENV is indistinguishable, at runtime, from one the user
# passed with `docker run -e`, so internal/config must treat it as an explicit
# setting — which turns every LocalStack-compatibility alias that disagrees
# with it (DEFAULT_REGION, EDGE_PORT, GATEWAY_LISTEN, DEBUG — see
# internal/config/localstack_aliases.go) into a startup conflict, breaking the
# drop-in migration promise (docs/migration-from-localstack.md) exactly in the
# images migrators run. This block used to restate the binary's own defaults
# (port 4566, region us-east-1, account 000000000000, log level info, debug
# off, and a 0.0.0.0 bind — which the marker below already selects, see
# resolveListenDefault in internal/config, #761), so removing them changed no
# effective default. TestDockerfileBakesNoConfigDefaults (internal/config)
# enforces that none creep back in.
#
# OVERCAST_DATA_DIR is the one genuinely image-specific default (/data rather
# than the native ~/.overcast/data), and OVERCAST_DATA_DIR_SOURCE=image marks
# it as the image's own default rather than user intent: the DATA_DIR alias
# overrides it instead of conflicting with it, and OVERCAST_STATE=auto does
# not read it as a persistence signal (see internal/config).
ENV OVERCAST_DATA_DIR=/data \
    OVERCAST_DATA_DIR_SOURCE=image

# OVERCAST_STATE is intentionally NOT set here — it defaults to "auto" (see
# internal/config/state_auto.go), which resolves to hybrid when a volume or
# bind mount is present at OVERCAST_DATA_DIR (or an existing database is
# found there), and memory otherwise. OVERCAST_DATA_DIR_SOURCE=image above
# marks OVERCAST_DATA_DIR=/data as the image's own baked-in default rather
# than user intent, so `docker run` with no volume mounted still resolves to
# memory (fast, ephemeral — what a volume-less run and most CI usage want),
# while `docker run -v vol:/data ...` resolves to hybrid (persistent) with
# zero configuration. Set OVERCAST_STATE explicitly to override either way.
#
# This stage is shared, and the paragraph above describes the CONSOLE image
# only. The slim stage below carries a `nosqlite` binary, and hybrid/persistent
# need SQLite: there, auto short-circuits to memory whatever is mounted, and
# hybrid/persistent refuse to start. wal is the durable backend that still
# works. Do not add an OVERCAST_STATE default here to "fix" that — a default
# would have to differ per stage, and the auto resolver already reports the
# reason at startup. See docs/storage.md § Builds without SQLite.

EXPOSE 4566

# Covers both modes; --no-check-certificate is fine here: this is a liveness
# probe against our own loopback, not a trust decision.
#
# https is tried FIRST, and the order is the whole point. A plain-HTTP probe
# against a TLS listener makes the daemon log
# "http: TLS handshake error ...: client sent an HTTP request to an HTTPS
# server" — once every interval, forever, for a perfectly healthy container,
# which buries the handshake errors that do mean something. The reverse costs
# nothing: an https probe against a plain-HTTP listener is refused client-side
# (wrong version number) and the server logs nothing at all.
HEALTHCHECK --interval=5s --timeout=3s --start-period=2s --retries=3 \
    CMD wget -qO- --no-check-certificate https://localhost:4566/_overcast/health || wget -qO- http://localhost:4566/_overcast/health || exit 1

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]

# ---- Stage 6: slim (headless — CI pipelines) -------------------------------
FROM base AS slim

COPY --from=go-builder-slim /overcast /usr/local/bin/overcast

# ---- Stage 7: console (default — with embedded web UI) ---------------------
FROM base

COPY --from=go-builder-console /overcast /usr/local/bin/overcast

# The Go binary serves the web console on port 4567 (configurable via
# OVERCAST_UI_PORT). The BFF is a pure-Go layer inside the same process.
EXPOSE 4567
