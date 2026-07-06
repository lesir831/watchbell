FROM node:24-bookworm-slim AS web
WORKDIR /src/web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.24-bookworm AS backend
WORKDIR /src
RUN apt-get update \
  && apt-get install -y --no-install-recommends gcc libc6-dev \
  && rm -rf /var/lib/apt/lists/*
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
RUN CGO_ENABLED=1 go build -buildvcs=false -trimpath -ldflags="-s -w" -o /out/watchbell ./cmd/watchbell

FROM debian:bookworm-slim
RUN apt-get update \
  && apt-get install -y --no-install-recommends ca-certificates tzdata \
  && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=backend /out/watchbell /usr/local/bin/watchbell
COPY --from=web /src/web/dist ./web/dist
ENV WATCHBELL_ADDR=:8080
ENV WATCHBELL_DB=/data/watchbell.db
ENV WATCHBELL_WEB_DIR=/app/web/dist
VOLUME ["/data"]
EXPOSE 8080
CMD ["watchbell"]
