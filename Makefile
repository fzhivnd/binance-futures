.PHONY: build run build-dashboard run-dashboard test migrate-up migrate-down docker-up docker-up-all docker-down lint

build:
	go build -o bin/bot ./cmd/bot

run: build
	./bin/bot -config config/config.yaml

build-dashboard:
	go build -o bin/dashboard ./cmd/dashboard

run-dashboard: build-dashboard
	./bin/dashboard

test:
	go test ./internal/... -v -race

migrate-up:
	migrate -path migrations -database "postgres://futures:futures_dev@localhost:5432/futures_bot?sslmode=disable" up

migrate-down:
	migrate -path migrations -database "postgres://futures:futures_dev@localhost:5432/futures_bot?sslmode=disable" down

docker-up:
	docker compose up -d postgres redis

docker-up-all:
	docker compose up -d

docker-down:
	docker compose down

lint:
	golangci-lint run ./...
