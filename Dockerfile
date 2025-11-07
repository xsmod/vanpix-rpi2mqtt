# Build stage
FROM golang:1.25-alpine AS build

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG VCS_REF=unknown

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .

# Build static-ish binary for the requested target (fallback to linux/amd64 if args are empty)
ENV CGO_ENABLED=0
RUN GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags "-s -w -X main.BuildTag=${VERSION} -X main.BuildCommit=${VCS_REF}" \
    -o /vanpix-rpi2mqtt ./main.go

# Runtime stage
FROM alpine:3.20

# Labels for OCI compliance (filled via build args by GoReleaser)
ARG VERSION=dev
LABEL org.opencontainers.image.title="vanpix-rpi2mqtt" \
      org.opencontainers.image.description="Raspberry Pi host stats to MQTT with Home Assistant discovery" \
      org.opencontainers.image.version=$VERSION \
      org.opencontainers.image.licenses="MIT"

# Expose version at runtime via APP_VERSION (used by getSWVersion override)
ENV APP_VERSION=$VERSION

RUN apk add --no-cache ca-certificates
COPY --from=build /vanpix-rpi2mqtt /usr/local/bin/vanpix-rpi2mqtt
USER 1000:1000
ENTRYPOINT ["/usr/local/bin/vanpix-rpi2mqtt"]
