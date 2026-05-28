FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o bin/bot ./cmd/bot
RUN go build -o bin/dashboard ./cmd/dashboard

FROM alpine:3.20
WORKDIR /app
COPY --from=builder /app/bin/bot .
COPY --from=builder /app/bin/dashboard .
COPY config/config.yaml config/
COPY migrations/ migrations/
ENTRYPOINT ["./bot", "-config", "config/config.yaml"]
