# syntax=docker/dockerfile:1.7

FROM golang:1.25-alpine AS builder

WORKDIR /src

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildDate=${BUILD_DATE}" \
      -o /out/network-list-sync \
      .

FROM alpine:3.22

RUN apk add --no-cache su-exec

WORKDIR /data

COPY --from=builder /out/network-list-sync /network-list-sync
COPY --chmod=755 docker-entrypoint.sh /docker-entrypoint.sh

VOLUME ["/data"]
EXPOSE 8080

ENTRYPOINT ["/docker-entrypoint.sh"]
CMD ["-addr", ":8080", "-db", "/data/sync.db", "-log-file", "/data/sync.log"]
