# syntax=docker/dockerfile:1
# Build the CGO binary on Ubuntu 22.04 to match the Xavier runtime's glibc.
ARG GO_IMAGE=golang:1.26.9-bookworm
ARG ORT_IMAGE=onnxruntime-jetson-xavier:1.22.2-cuda12.2
FROM ${GO_IMAGE} AS toolchain
FROM ubuntu:22.04 AS build
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates build-essential git && rm -rf /var/lib/apt/lists/*
COPY --from=toolchain /usr/local/go /usr/local/go
ENV PATH=/usr/local/go/bin:$PATH GOTOOLCHAIN=local GOPATH=/go
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG VERSION=devel
ARG BAKE_MODELS=1
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 go build -trimpath -ldflags "-s -w -X github.com/androiddrew/parakeet-run/internal/cli.version=$VERSION" -o /out/parakeet-run ./cmd/parakeet-run && \
    go run scripts/collect-licenses.go /out/licenses
RUN mkdir /assets && if [ "$BAKE_MODELS" = 1 ]; then /out/parakeet-run pull --dir /assets; fi
FROM ${ORT_IMAGE}
ARG VERSION=devel
LABEL org.opencontainers.image.title="parakeet-run" org.opencontainers.image.version="$VERSION" \
      org.opencontainers.image.source="https://github.com/androiddrew/parakeet-run" org.opencontainers.image.licenses="Apache-2.0"
RUN apt-get update && apt-get install -y --no-install-recommends ffmpeg ca-certificates && rm -rf /var/lib/apt/lists/* && \
    useradd --uid 10001 --user-group --groups video --home-dir /var/lib/parakeet-run --no-create-home --shell /usr/sbin/nologin parakeet-run && \
    test "$(getent group video | cut -d: -f3)" = 44
COPY --from=build /out/parakeet-run /usr/local/bin/parakeet-run
COPY --from=build /out/licenses/ /usr/share/doc/parakeet-run/go-dependencies/
COPY LICENSE NOTICE THIRD_PARTY.md ASSETS.md /usr/share/doc/parakeet-run/
COPY third_party/ /usr/share/doc/parakeet-run/third_party/
COPY docker/jetson-xavier/config.yaml /etc/parakeet-run/config.yaml
COPY --from=build --chown=10001:10001 /assets/ /var/lib/parakeet-run/assets/
ENV PARAKEET_RUN_CONFIG=/etc/parakeet-run/config.yaml
USER parakeet-run
WORKDIR /var/lib/parakeet-run
EXPOSE 8880
VOLUME /var/lib/parakeet-run/assets
HEALTHCHECK --interval=30s --timeout=10s --start-period=300s CMD ["parakeet-run", "_healthcheck"]
ENTRYPOINT ["parakeet-run"]
CMD ["serve"]
