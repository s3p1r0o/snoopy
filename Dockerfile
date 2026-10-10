# syntax=docker/dockerfile:1
# Two deliverables from one Dockerfile:
#   - default target ("harness"; the LAST stage is the default build target):
#     the Go proxy + DeepSeek Harness (dsh, official Node build) supervised by
#     the launcher; OpenAI endpoint and Web UI exposed.
#   - --target proxy: the proxy alone on a distroless base (as before).
# Pinned by digests (multi-arch image indexes); update deliberately.
# Multi-arch: `docker buildx build --platform linux/amd64,linux/arm64` (or
# `just docker-buildx`). The Go stage runs on the build host and
# cross-compiles to TARGETOS/TARGETARCH; the later stages run per target.
FROM --platform=$BUILDPLATFORM golang:1.26@sha256:52d57e3542f28ad72e9a1084e209366d8ca6af04997636134c138d16702a70a6 AS build-go
ARG TARGETOS TARGETARCH
ARG VERSION=unknown
WORKDIR /src
COPY proxy/go.mod proxy/go.sum ./
RUN go mod download
COPY proxy/ .
RUN CGO_ENABLED=0 GOOS="${TARGETOS:-linux}" GOARCH="${TARGETARCH:-amd64}" go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /proxy .

# --target proxy: the proxy alone (distroless, non-root), the pre-harness shape.
# The CA bundle travels along: gRPC-TLS to admin.rilldata.com needs it and the
# slim/distroless runtimes ship without a usable root store.
FROM gcr.io/distroless/static-debian12@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab AS proxy
COPY --from=build-go /proxy /proxy
COPY --from=build-go /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
EXPOSE 8081
USER nonroot:nonroot
ENTRYPOINT ["/proxy"]

# Bundled web_search engine: SearXNG (AGPL-3.0-or-later), run UNMODIFIED as
# its own process by the launcher, on 127.0.0.1 inside the container (see
# lib/searxng.js). Pinned to one upstream commit, fetched by git (the commit
# hash pins the content); built on the same Debian base as the runtime, so its
# virtualenv's python3 is the one the runtime installs. Its source and licence
# ship in /opt/searxng (searx/, LICENSE); upstream: github.com/searxng/searxng.
FROM node:24-slim@sha256:d6aa754f16b3197301076f047b5def2f02ea1dbbc2ca920407d46d7ec7f87b20 AS searxng
ARG SEARXNG_COMMIT=3cd69d30e2a78dfc817be9e349e7c2e4317c92e3
ARG SEARXNG_VERSION=2026.9.23+3cd69d30e
RUN apt-get update && apt-get install -y --no-install-recommends python3 python3-venv \
    && rm -rf /var/lib/apt/lists/*
ADD --keep-git-dir=false https://github.com/searxng/searxng.git#${SEARXNG_COMMIT} /src/searxng
RUN set -eux; \
    mkdir -p /opt/searxng; \
    cp -r /src/searxng/searx /src/searxng/LICENSE /src/searxng/requirements.txt /src/searxng/requirements-server.txt /opt/searxng/; \
    python3 -m venv /opt/searxng/.venv; \
    /opt/searxng/.venv/bin/pip install --no-cache-dir --disable-pip-version-check \
        -r /opt/searxng/requirements.txt -r /opt/searxng/requirements-server.txt; \
    printf '%s\n' \
        '# SPDX-License-Identifier: AGPL-3.0-or-later' \
        '# written by the snoopy Dockerfile (no .git in the image), as searx/version.py freeze would' \
        "VERSION_STRING = \"${SEARXNG_VERSION}\"" \
        "VERSION_TAG = \"${SEARXNG_VERSION}\"" \
        "DOCKER_TAG = \"${SEARXNG_VERSION}\"" \
        'GIT_URL = "https://github.com/searxng/searxng"' \
        'GIT_BRANCH = "master"' \
        > /opt/searxng/searx/version_frozen.py

# Node official build (glibc: dsh native packages only ship for linux/darwin).
# npm deps install WITHOUT scripts: the proxy binary comes from build-go, not
# from a postinstall download — the image build stays offline and hermetic.
FROM node:24-slim@sha256:d6aa754f16b3197301076f047b5def2f02ea1dbbc2ca920407d46d7ec7f87b20 AS deps-base
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --omit=dev --ignore-scripts --no-audit --no-fund
COPY bin/ bin/
COPY lib/ lib/
COPY profile/ profile/
COPY skills/ skills/
COPY --from=build-go /proxy bin/proxy
# CA bundle for the proxy's gRPC-TLS to admin.rilldata.com (slim lacks it on
# some bases; be explicit either way).
COPY --from=build-go /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
# The dsh home volume, pre-created owned by the runtime user (USER node).
RUN mkdir -p /data && chown node:node /data

# Default build target: snoopy — the standalone harness — Go proxy + dsh + supervisor.
# dsh REFUSES non-loopback --host by upstream design (agent UI = RCE surface):
# the Web UI stays loopback-in-container and is meant to be reached with
# `docker run --network host` (loopback on the host namespace) or a tunnel;
# the OpenAI endpoint widens to all interfaces for bridge deployments.
FROM deps-base AS harness
ARG VERSION=unknown
# python3 runs the bundled SearXNG (same Debian release as the searxng stage).
RUN apt-get update && apt-get install -y --no-install-recommends python3 \
    && rm -rf /var/lib/apt/lists/*
COPY --from=searxng /opt/searxng /opt/searxng
ENV DSH_HOME=/data/dsh \
    SNOOPY_PROXY_ADDRESS=0.0.0.0 \
    SNOOPY_PORT=8081 \
    SNOOPY_SEARXNG_DIR=/opt/searxng
VOLUME /data
EXPOSE 8081 3080
USER node
ENTRYPOINT ["node", "bin/launcher.js"]
CMD ["web", "--no-open", "--port", "3080"]