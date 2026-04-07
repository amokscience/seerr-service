# ---- Build stage ----
FROM golang:1.22-alpine AS builder

WORKDIR /app

# Cache module downloads
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build a statically linked binary
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o /seerr-service ./cmd/main.go

# ---- Runtime stage ----
FROM scratch

# Pull in CA certs so outbound HTTPS works
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

COPY --from=builder /seerr-service /seerr-service

EXPOSE 8080

ENTRYPOINT ["/seerr-service"]
