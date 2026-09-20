# syntax=docker/dockerfile:1

# Shipwick agent image.
#
#   docker build -t ghcr.io/shipwick/agent .
#
# The build stage runs on the builder's own architecture and cross-compiles:
# pure Go, so a multi-platform build needs no emulation.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY pkg ./pkg
COPY agent ./agent

ARG VERSION=dev
ARG TARGETOS TARGETARCH
# CGO is off: the SQLite driver is pure Go, so the binary is fully static.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags "-s -w -X github.com/shipwick/shipwick/pkg/version.Version=${VERSION}" \
      -o /out/shipwick-agent ./agent/cmd/shipwick-agent

# No shell, no package manager: nothing in the image but the agent and CA
# certificates. The agent runs as root because it needs the Docker socket,
# which already is root-equivalent access to the host (see README, Security).
FROM gcr.io/distroless/static-debian12
COPY --from=build /out/shipwick-agent /usr/local/bin/shipwick-agent

ENV SHIPWICK_LISTEN_ADDR=0.0.0.0:9000 \
    SHIPWICK_DATA_DIR=/var/lib/shipwick
VOLUME /var/lib/shipwick
EXPOSE 9000

HEALTHCHECK --interval=10s --timeout=5s --start-period=5s --retries=3 \
    CMD ["/usr/local/bin/shipwick-agent", "healthcheck"]

ENTRYPOINT ["/usr/local/bin/shipwick-agent"]
