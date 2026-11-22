# ---- build ----
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/storyboard ./cmd/server

# ---- runtime: Alpine + ffmpeg (final render), non-root ----
FROM alpine:3.22
RUN apk add --no-cache ffmpeg ca-certificates tzdata \
 && addgroup -S -g 10001 app && adduser -S -D -H -u 10001 -G app app \
 && mkdir -p /data && chown app:app /data
WORKDIR /app
COPY --from=build /out/storyboard /app/storyboard
# License texts travel with the image (AGPL-3.0, dual-license notice, third-party notices incl. FFmpeg)
COPY LICENSE NOTICE THIRD_PARTY_NOTICES.md THIRD_PARTY_LICENSES.txt /app/licenses/
LABEL org.opencontainers.image.title="Storyboard Studio" \
      org.opencontainers.image.licenses="AGPL-3.0-only OR LicenseRef-Storyboard-Commercial"

ENV APP_ADDR=0.0.0.0:8080 \
    DATA_DIR=/data \
    TRUST_PROXY=true \
    COOKIE_SECURE=true \
    FFMPEG_PATH=/usr/bin/ffmpeg \
    FFPROBE_PATH=/usr/bin/ffprobe

USER app:app
EXPOSE 8080
VOLUME ["/data"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 CMD ["/app/storyboard", "healthcheck"]
ENTRYPOINT ["/app/storyboard"]
