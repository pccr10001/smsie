# Build Stage
FROM golang:1.24-alpine AS builder

WORKDIR /app

# Install git if needed for dependencies (though go mod tidy should have handled it)
# Alpine base is fine for CGO_ENABLED=0
RUN apk add --no-cache git

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Build the binary
# CGO_ENABLED=0 is preferred for static binary, and we are using glebarez/sqlite which is pure Go.
RUN CGO_ENABLED=0 GOOS=linux go build -o smsie main.go

# Runtime Stage
FROM alpine:latest

WORKDIR /app

# Install any runtime dependencies if needed (e.g. ca-certificates for HTTPS/Webhooks)
RUN apk add --no-cache ca-certificates tzdata

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
