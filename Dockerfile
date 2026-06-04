# Build stage
FROM golang:1.25-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /bin/bot ./cmd/bot
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /bin/dashboard ./cmd/dashboard

# Runtime stage
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY --from=builder /bin/bot .
COPY --from=builder /bin/dashboard .

COPY config/config.yaml ./config/config.yaml
COPY migrations ./migrations
