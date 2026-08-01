# syntax=docker/dockerfile:1

# ── Build ─────────────────────────────────────────────────────────────────
FROM --platform=$BUILDPLATFORM golang:1.26.5-bookworm AS build

WORKDIR /src

# Dependencies first, so a source-only change reuses the module layer.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .

ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown

# turso-go embeds a prebuilt engine for every platform it supports — around
# 150 MB, of which this image can use exactly one. Vendor the module and drop
# the rest before compiling, which takes the binary from ~165 MB to ~15 MB.
# The engine is loaded via purego at runtime, so the build itself stays CGO-free.
RUN --mount=type=cache,target=/go/pkg/mod go mod vendor && \
    LIBS=vendor/github.com/tursodatabase/turso-go/libs && \
    find $LIBS -mindepth 1 -maxdepth 1 -type d ! -name "linux_${TARGETARCH}" -exec rm -rf {} + && \
    ls -la $LIBS

RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -mod=vendor -trimpath \
      -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildTime=${BUILD_TIME}" \
      -o /out/pool ./cmd/server

# Stage the data directory here, because the runtime image has no shell to
# mkdir with and the nonroot user must be able to write to it.
RUN mkdir -p /staging/data && chown -R 65532:65532 /staging/data

# ── Runtime ───────────────────────────────────────────────────────────────
# distroless/cc rather than /base: turso-go dlopens a Rust shared object that
# links libgcc_s, which /base does not ship. Alpine is out entirely — turso
# publishes only a static archive for musl, which purego cannot dlopen.
FROM gcr.io/distroless/cc-debian12:nonroot

COPY --from=build /out/pool /usr/local/bin/pool
COPY --from=build --chown=65532:65532 /staging/data /data

ENV POOL_ADDR=:8080 \
    POOL_DB_PATH=/data/pool.db \
    POOL_DATA_DIR=/data \
    POOL_ENV=production

VOLUME /data
EXPOSE 8080
USER nonroot

# No shell in the image, so the binary probes itself.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/usr/local/bin/pool", "-healthcheck"]

ENTRYPOINT ["/usr/local/bin/pool"]
