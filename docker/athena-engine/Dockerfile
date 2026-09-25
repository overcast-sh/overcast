# syntax=docker/dockerfile:1@sha256:ecfaec9ed6d810b56388c508f4121597bfbba70d41a6dfeee4d8cad5f295fc32
#
# Athena's query engine: Trino with only the two plugins the engine loads.
#
# trinodb/trino-core is Trino's server and CLI with no connectors; this adds
# the hive and iceberg plugins, which is the recipe of the custom-docker
# example in github.com/trinodb/trino-packages. The stock trinodb/trino image
# carries every plugin Trino ships, which the engine never loads: 1.04 GB to
# pull for linux/amd64, against 0.78 GB for this one.
#
# Every input is pinned: the frontend and both bases by digest (each the
# multi-arch index, so buildx still resolves per platform), and each plugin
# zip by the SHA-256 GitHub records for its release asset. Trino stopped
# publishing plugin zips to Maven Central with 477, so the GitHub release is
# where they come from. The image is a function of this file.
#
# Build for the current platform from the repository root (Linux, macOS or
# Windows, with buildx — the default builder of any current Docker):
#   docker buildx build --load -t overcast-athena-engine:dev docker/athena-engine
# Run Athena's engine tests on it:
#   ATHENA_ENGINE_IMAGE=overcast-athena-engine:dev go test ./tests/integration/athenaengine/...
#   (PowerShell: $env:ATHENA_ENGINE_IMAGE = 'overcast-athena-engine:dev', then the go test)
# and remove it afterwards: docker rmi overcast-athena-engine:dev
# CI builds linux/amd64 and linux/arm64 and publishes them to
# ghcr.io/overcast-sh/overcast-athena-engine (.github/workflows/athena-engine-image.yml).
#
# Moving to another Trino release changes three things together, and the
# build fails if the first two disagree: the trino-core tag and digest,
# TRINO_VERSION, and both plugin checksums —
#   docker buildx imagetools inspect trinodb/trino-core:<v> --format '{{.Manifest.Digest}}'
#   gh api repos/trinodb/trino/releases/tags/<v> --jq '.assets[] | select(.name | test("^trino-(hive|iceberg)-")) | [.name, .digest] | @tsv'
# Renovate is told to leave the trino-core version alone for that reason
# (.github/renovate.json5); it still advances the digest of the pinned tag.

ARG TRINO_VERSION=483

# ---- Plugins -----------------------------------------------------------------
# Fetched and unpacked on the build platform: the zips are architecture-free
# Java. Only the final stage's check and rm run under emulation when building
# for another platform.
FROM --platform=$BUILDPLATFORM alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b AS plugins
ARG TRINO_VERSION
ADD --checksum=sha256:43201c15d749d060a347289e0829734bcc70b7e0c38de6fb4d152631c833cc48 \
    https://github.com/trinodb/trino/releases/download/${TRINO_VERSION}/trino-hive-${TRINO_VERSION}.zip /zips/hive.zip
ADD --checksum=sha256:59b4b9c5013b6df4b4459583aff6f4cb97c40cab9edf8d133dca04ea869b1140 \
    https://github.com/trinodb/trino/releases/download/${TRINO_VERSION}/trino-iceberg-${TRINO_VERSION}.zip /zips/iceberg.zip
# Each zip holds one directory, trino-<plugin>-<version>, which Trino wants
# named after the plugin; its directories come out world-writable, and are
# reset to the modes the base image uses. The two plugins share most of their
# jars (Hadoop, the AWS SDK, Parquet and ORC): identical files are hard-linked,
# which a layer stores once.
RUN set -eu; \
    mkdir /plugin; \
    for plugin in hive iceberg; do \
        unzip -q "/zips/$plugin.zip" -d /unpacked; \
        mv "/unpacked/trino-$plugin-$TRINO_VERSION" "/plugin/$plugin"; \
    done; \
    chmod -R u=rwX,go=rX /plugin; \
    cd /plugin; \
    find . -type f -exec sha256sum {} + | sort | \
        awk '$1 == seen { print kept, $2; next } { seen = $1; kept = $2 }' | \
        while read -r kept dup; do ln -f "$kept" "$dup"; done

# ---- Engine ------------------------------------------------------------------
# trino-core brings a few plugins of its own (exchange-filesystem, geospatial,
# functions-python and the like). Trino loads every directory under its plugin
# directory, so they are removed and hive and iceberg are the only two left.
# The removal hides them rather than reclaiming their space (about 170 MB
# unpacked), which would take flattening the base and restating its config.
FROM trinodb/trino-core:483@sha256:de496986c346c9cac3435684c184742079e833d8333e14b76cd2d2044e6550fb
ARG TRINO_VERSION
RUN set -eu; \
    test -f "/usr/lib/trino/lib/io.trino_trino-main-$TRINO_VERSION.jar" || { \
        echo "trino-core is not Trino $TRINO_VERSION: move FROM and TRINO_VERSION together" >&2; exit 1; }; \
    rm -rf /usr/lib/trino/plugin/*
COPY --from=plugins --chown=trino:trino /plugin/ /usr/lib/trino/plugin/
