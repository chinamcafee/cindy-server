.PHONY: run build test migrate seed keys compose-up compose-down tidy fmt vet

run:
	go run ./cmd/server

build:
	mkdir -p bin && go build -o bin/cindy-server ./cmd/server

test:
	go test ./...

test-integration:
	CINDY_TEST_DATABASE_URL="postgres://cindy:cindy_dev_password@127.0.0.1:5432/cindy?sslmode=disable" go test ./...

migrate:
	go run ./cmd/migrate

seed:
	go run ./cmd/seed

keys:
	@mkdir -p .keys
	@if [ ! -f .keys/jwt-ed25519.pem ]; then \
		go run ./cmd/keys; \
	else \
		echo ".keys/jwt-ed25519.pem already exists"; \
	fi

compose-up:
	docker compose -p cindy-middlewares -f deploy/compose/docker-compose.yml up -d

compose-down:
	docker compose -p cindy-middlewares -f deploy/compose/docker-compose.yml down

tidy:
	go mod tidy

fmt:
	gofmt -w ./cmd ./internal ./migrations

vet:
	go vet ./...
