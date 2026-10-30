# Membrane: multi-stage, distroless, non-root container image.
#
# Build:
#   docker build \
#     --build-arg VERSION=$(git describe --tags --always --dirty) \
#     --build-arg COMMIT=$(git rev-parse --short HEAD) \
#     --build-arg BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ) \
#     -t inferencepath/membrane:dev .
#
# The image contains only the statically-linked binary (no shell, no package
# manager) and runs as a non-root user. Configuration and secrets are provided
# at runtime (mounted config + env/file/vault secret references), never baked in.
#
# The Membrane Console (operator UI) is a static single-page app committed under
# internal/server/console/webui/dist and compiled into the binary via go:embed,
# so there is no separate web/Node build stage. When enabled it binds a separate
# admin listener (default 127.0.0.1:8300), off the /v1/chat egress path.

# go.mod requires Go 1.26 (pulled in by the pure-Go SQLite state store).
ARG GO_VERSION=1.26

FROM golang:${GO_VERSION}-bookworm AS build
WORKDIR /src

# Cache dependencies first.
COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY . .

ARG VERSION=0.0.0-dev
ARG COMMIT=none
ARG BUILD_TIME=unknown
# CGO disabled -> a static binary that runs on distroless/scratch (the SQLite
# driver is modernc.org/sqlite, a pure-Go implementation, so no cgo).
ENV CGO_ENABLED=0
RUN go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildTime=${BUILD_TIME}" \
      -o /out/membrane ./cmd/membrane

# Pre-create the managed state directory owned by the non-root runtime user, so
# a named volume mounted here is writable without any entrypoint chown. Docker
# seeds a fresh named volume with this directory's ownership/permissions.
RUN install -d -o 65532 -g 65532 -m 0700 /var/lib/membrane

# Minimal, non-root runtime. distroless provides CA certs for TLS egress.
FROM gcr.io/distroless/static-debian12:nonroot

LABEL org.opencontainers.image.title="Membrane" \
      org.opencontainers.image.description="Self-hosted AI gateway that anonymizes inference calls at the egress boundary." \
      org.opencontainers.image.licenses="BUSL-1.1" \
      org.opencontainers.image.source="https://github.com/inferencepath/membrane"

COPY --from=build /out/membrane /usr/local/bin/membrane
# The managed state directory, owned by the non-root runtime user. Declared as a
# VOLUME so the embedded store (membrane.db) persists across container restarts;
# back it with a named volume or bind mount (see docker-compose.yml).
COPY --from=build --chown=65532:65532 /var/lib/membrane /var/lib/membrane
VOLUME ["/var/lib/membrane"]

# Non-root (uid 65532 from the distroless :nonroot tag).
USER nonroot:nonroot
# 8080: the data-plane egress API (/v1/chat). 8300: the optional Membrane Console
# admin listener (bind to loopback or protect with an admin token when exposed).
EXPOSE 8080 8300

ENTRYPOINT ["/usr/local/bin/membrane"]
CMD ["--config", "/etc/inferencepath/membrane.yaml"]
