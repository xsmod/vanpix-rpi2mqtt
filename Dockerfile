# Build stage
FROM golang:1.25-alpine AS build

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .

# Build static-ish binary for the requested target (fallback to linux/amd64 if args are empty)
ENV CGO_ENABLED=0
RUN GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} go build -trimpath -ldflags "-s -w" -o /rpi-stats2mqtt ./main.go

# Runtime stage
FROM alpine:3.20

# Labels for OCI compliance (filled via build args by GoReleaser)
ARG VERSION=dev
LABEL org.opencontainers.image.title="rpi-stats2mqtt" \
      org.opencontainers.image.description="Raspberry Pi host stats to MQTT with Home Assistant discovery" \
      org.opencontainers.image.version=$VERSION \
      org.opencontainers.image.licenses="MIT"

RUN apk add --no-cache ca-certificates
COPY --from=build /rpi-stats2mqtt /usr/local/bin/rpi-stats2mqtt
USER 1000:1000
ENTRYPOINT ["/usr/local/bin/rpi-stats2mqtt"]
