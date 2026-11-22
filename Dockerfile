# syntax=docker/dockerfile:1

FROM node:22.23.5-alpine3.23 AS frontend
WORKDIR /build/web
RUN corepack enable
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

FROM golang:1.26.11-alpine3.23 AS backend
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY web/embed.go ./web/embed.go
COPY --from=frontend /build/web/static/app ./web/static/app
RUN CGO_ENABLED=0 GOTOOLCHAIN=local go build -trimpath -ldflags="-s -w" -o /out/ledgergate ./cmd/ledgergate

FROM alpine:3.23.8 AS runtime
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S -g 10001 ledgergate \
    && adduser -S -D -H -u 10001 -G ledgergate ledgergate \
    && mkdir -p /data \
    && chown 10001:10001 /data \
    && chmod 0700 /data
WORKDIR /app
COPY --from=backend /out/ledgergate /usr/local/bin/ledgergate
ENV LEDGERGATE_DATA_DIR=/data \
    TZ=Asia/Shanghai
USER 10001:10001
EXPOSE 8080
VOLUME ["/data"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["/usr/local/bin/ledgergate"]
