ARG GO_VERSION=1.24

FROM golang:${GO_VERSION} AS builder

ARG TARGETOS=linux
ARG TARGETARCH=amd64

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o /tmp/bftscout ./cmd/monitor

FROM debian:12-slim AS runtime

WORKDIR /app

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/* \
    && groupadd --system bftscout \
    && useradd --system --gid bftscout --home /app --shell /usr/sbin/nologin bftscout

COPY --from=builder /tmp/bftscout /usr/local/bin/bftscout

ENV RPC_URL=http://localhost:26657 \
    WS_PATH=/websocket \
    DATABASE_URL= \
    APP_API_URL=http://localhost:1317

USER bftscout:bftscout

ENTRYPOINT ["/usr/local/bin/bftscout"]

