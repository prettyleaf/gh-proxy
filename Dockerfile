# syntax=docker/dockerfile:1

# The binary is pure Go, so the build stage always runs on the native
# architecture of the runner and cross-compiles. No QEMU, no per-arch runners.
FROM --platform=$BUILDPLATFORM golang:1.23-alpine AS build

RUN apk add --no-cache ca-certificates

WORKDIR /src
# No third-party dependencies, so there is no module download step to cache.
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal

ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
        -trimpath \
        -ldflags "-s -w -X main.version=${VERSION}" \
        -o /out/gh-proxy ./cmd/gh-proxy

# scratch: no shell, no package manager, nothing to pivot to if the proxy is
# ever compromised. The CA bundle is the only thing it needs, for TLS to GitHub.
FROM scratch

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/gh-proxy /gh-proxy

USER 65534:65534
EXPOSE 8899

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD ["/gh-proxy", "healthcheck"]

ENTRYPOINT ["/gh-proxy"]
