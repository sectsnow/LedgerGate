# syntax=docker/dockerfile:1

FROM node:22.23.5-alpine3.23 AS frontend
WORKDIR /build/web
RUN corepack enable
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

FROM golang:1.26.11-alpine3.23 AS backend
ARG VCS_REF
ARG BUILD_DATE
ARG VCS_MODIFIED
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY web/embed.go ./web/embed.go
COPY web/package.json ./web/package.json
COPY --from=frontend /build/web/static/app ./web/static/app
RUN ledgergate_build_time="${BUILD_DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}" \
    && CGO_ENABLED=0 GOTOOLCHAIN=local go build -trimpath \
       -ldflags="-s -w -X ledgergate/internal/app.buildRevision=${VCS_REF} -X ledgergate/internal/app.buildTime=${ledgergate_build_time} -X ledgergate/internal/app.buildModified=${VCS_MODIFIED}" \
       -o /out/ledgergate ./cmd/ledgergate

FROM alpine:3.23.8 AS runtime
RUN apk add --no-cache ca-certificates tzdata \
    && mkdir -p /data \
    && chmod 0700 /data
WORKDIR /app
COPY --from=backend /out/ledgergate /usr/local/bin/ledgergate
ENV LEDGERGATE_DATA_DIR=/data \
    TZ=Asia/Shanghai
USER 0:0
EXPOSE 8080
VOLUME ["/data"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["/usr/local/bin/ledgergate"]
