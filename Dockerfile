# syntax=docker/dockerfile:1.7

ARG VERSION=dev
ARG VCS_REF=unknown
ARG BUILD_DATE=unknown
ARG SOURCE_URL=https://github.com/watchbell/watchbell

FROM node:24-bookworm-slim AS web
WORKDIR /src/web
COPY web/package*.json ./
RUN --mount=type=cache,target=/root/.npm npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.24-bookworm AS backend
ARG VERSION
ARG VCS_REF
ARG BUILD_DATE
WORKDIR /src
RUN apt-get update \
  && apt-get install -y --no-install-recommends gcc libc6-dev \
  && rm -rf /var/lib/apt/lists/*
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
RUN --mount=type=cache,target=/go/pkg/mod \
  --mount=type=cache,target=/root/.cache/go-build \
  CGO_ENABLED=1 go build -buildvcs=false -trimpath \
  -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${VCS_REF} -X main.buildDate=${BUILD_DATE}" \
  -o /out/watchbell ./cmd/watchbell

FROM debian:bookworm-slim
ARG VERSION
ARG VCS_REF
ARG BUILD_DATE
ARG SOURCE_URL
LABEL org.opencontainers.image.title="WatchBell" \
  org.opencontainers.image.description="Lightweight self-hosted monitoring and notifications" \
  org.opencontainers.image.version="${VERSION}" \
  org.opencontainers.image.revision="${VCS_REF}" \
  org.opencontainers.image.created="${BUILD_DATE}" \
  org.opencontainers.image.source="${SOURCE_URL}"
RUN apt-get update \
  && apt-get install -y --no-install-recommends ca-certificates tzdata \
  && rm -rf /var/lib/apt/lists/* \
  && groupadd --system --gid 10001 watchbell \
  && useradd --uid 10001 --gid watchbell --no-create-home --home-dir /app --shell /usr/sbin/nologin watchbell \
  && mkdir -p /app/web/dist /data \
  && chown -R watchbell:watchbell /app /data
WORKDIR /app
COPY --from=backend /out/watchbell /usr/local/bin/watchbell
COPY --from=web --chown=watchbell:watchbell /src/web/dist ./web/dist
ENV WATCHBELL_ADDR=:8080 \
  WATCHBELL_DB=/data/watchbell.db \
  WATCHBELL_WEB_DIR=/app/web/dist \
  WATCHBELL_HEALTHCHECK_URL=http://127.0.0.1:8080/api/health
VOLUME ["/data"]
EXPOSE 8080
USER watchbell
HEALTHCHECK --interval=30s --timeout=6s --start-period=10s --retries=3 CMD ["watchbell", "healthcheck"]
ENTRYPOINT ["watchbell"]
