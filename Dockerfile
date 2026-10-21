# Build a static binary, then ship it on a small base with CA certificates.
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/tal ./cmd/tal

FROM alpine:3.20
RUN adduser -D -u 10001 talaria && mkdir /data && chown talaria /data
COPY --from=build /out/tal /usr/local/bin/tal
USER talaria
# Notes, skills, sessions, jobs and per-chat choices live here: mount a volume.
ENV TALARIA_HOME=/data
VOLUME /data
ENTRYPOINT ["tal"]
CMD ["gateway"]
