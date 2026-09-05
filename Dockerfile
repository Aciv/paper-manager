# syntax=docker/dockerfile:1
FROM golang:1.26-alpine AS build
ARG HTTP_PROXY
ARG HTTPS_PROXY
ARG NO_PROXY
ARG GOPROXY=https://proxy.golang.org,direct
WORKDIR /src
COPY go.mod go.sum ./
# Docker Desktop may inject a stale lowercase proxy from ~/.docker/config.json.
# Keep proxy values build-scoped and explicitly mirror the Compose arguments to
# both spellings, so a current HTTP(S)_PROXY (or ALL_PROXY fallback) wins for
# Go module downloads without leaking proxy configuration into the runtime.
RUN --mount=type=cache,target=/go/pkg/mod \
    HTTP_PROXY="$HTTP_PROXY" HTTPS_PROXY="${HTTPS_PROXY:-$HTTP_PROXY}" NO_PROXY="$NO_PROXY" \
    http_proxy="$HTTP_PROXY" https_proxy="${HTTPS_PROXY:-$HTTP_PROXY}" no_proxy="$NO_PROXY" \
    GOPROXY="$GOPROXY" go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    HTTP_PROXY="$HTTP_PROXY" HTTPS_PROXY="${HTTPS_PROXY:-$HTTP_PROXY}" NO_PROXY="$NO_PROXY" \
    http_proxy="$HTTP_PROXY" https_proxy="${HTTPS_PROXY:-$HTTP_PROXY}" no_proxy="$NO_PROXY" \
    GOPROXY="$GOPROXY" CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/papers-api ./cmd/server

FROM alpine:3.20 AS runtime-assets

FROM scratch
WORKDIR /app
COPY --from=runtime-assets /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/papers-api /app/papers-api
ENV SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt
EXPOSE 8080
ENTRYPOINT ["/app/papers-api"]
