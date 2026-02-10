# Build Stage
FROM golang:1.25-bookworm AS builder

WORKDIR /app

ARG GO_BUILD_TAGS=""

RUN apt-get update && apt-get install -y --no-install-recommends \
    pkg-config \
    portaudio19-dev \
    libusb-1.0-0-dev \
    ffmpeg \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY go.mod go.sum ./
RUN GOTOOLCHAIN=auto go mod download

COPY . .

# Build CGO binary for target platform.
RUN GOTOOLCHAIN=auto CGO_ENABLED=1 go build ${GO_BUILD_TAGS:+-tags ${GO_BUILD_TAGS}} -o smsie main.go

# Runtime Stage
FROM debian:bookworm-slim

WORKDIR /app

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    tzdata \
    ffmpeg \
    libusb-1.0-0 \
    libportaudio2 \
    && rm -rf /var/lib/apt/lists/*

# Copy binary
COPY --from=builder /app/smsie .

# Copy static assets and template files
COPY --from=builder /app/web ./web

# Expose port
EXPOSE 8080

# Volume for database (optional persistence)
VOLUME ["/app/data"]

# Entrypoint
CMD ["./smsie"]
