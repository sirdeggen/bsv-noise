# syntax=docker/dockerfile:1

FROM node:22-bookworm AS console
WORKDIR /src/console
COPY console/package.json console/package-lock.json ./
RUN npm ci
COPY console/ ./
RUN npm run build

FROM golang:1.27.3-bookworm AS build
WORKDIR /src
RUN apt-get update \
    && apt-get install -y --no-install-recommends gcc libc6-dev \
    && rm -rf /var/lib/apt/lists/*
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=console /src/console/dist/index.html ./internal/httpserver/console.html
ENV CGO_ENABLED=1
RUN go build -trimpath -ldflags="-s -w" -o /out/brc29-loadgen ./cmd/brc29-loadgen

FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --uid 65532 --user-group --create-home --shell /usr/sbin/nologin app \
    && mkdir -p /data \
    && chown 65532:65532 /data
COPY --from=build /out/brc29-loadgen /usr/local/bin/brc29-loadgen
USER 65532:65532
WORKDIR /data
VOLUME /data
EXPOSE 8741
ENTRYPOINT ["/usr/local/bin/brc29-loadgen"]
