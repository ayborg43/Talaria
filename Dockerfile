# syntax=docker/dockerfile:1

# Build a static binary (no cgo), then ship it on a small base.
ARG GO_VERSION=1.27.3
FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X talaria/internal/version.Version=${VERSION}" -o /out/tal ./cmd/tal

FROM alpine:3.22
# What the agent's own shell commands can use. The container is where they run, so it is also what limits them.
# Chromium is here too, so the browser tools work on a server just as on a laptop: headless, with its own
# sandbox off (it cannot work unprivileged), persisting logins in the profile under the home volume.
RUN apk add --no-cache ca-certificates tzdata curl git chromium ttf-dejavu font-noto-emoji \
 && adduser -D -u 10001 -h /work talaria \
 && mkdir -p /data /work && chown talaria /data /work
COPY --from=build /out/tal /usr/local/bin/tal
COPY docker/entrypoint.sh /usr/local/bin/talaria-entrypoint
USER talaria
WORKDIR /work

# /data holds notes, skills, history, jobs, grants and the vault; /work is where the agent's files go.
# Mount a volume on each, or they are lost when the container is replaced.
ENV TALARIA_HOME=/data \
    HOME=/work \
    TALARIA_CONTAINER=1 \
    TALARIA_OPS_LISTEN=127.0.0.1:9100
VOLUME ["/data", "/work"]

# Healthy when the gateway answers on its own health endpoint (this machine only, so nothing is exposed).
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 \
  CMD wget -q -O /dev/null "http://${TALARIA_OPS_LISTEN:-127.0.0.1:9100}/healthz" || exit 1

LABEL org.opencontainers.image.title="Talaria" \
      org.opencontainers.image.description="A lightweight personal agent, controlled from Telegram or Discord" \
      org.opencontainers.image.source="https://github.com/ayborg43/Talaria"

ENTRYPOINT ["talaria-entrypoint"]
CMD ["gateway"]
