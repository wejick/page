.PHONY: up down run seed test test-integration tidy

# Infra for local development (MinIO; the database is a local SQLite file).
up:
	docker compose up -d --wait

down:
	docker compose down -v

DEV_ENV = SQLITE_PATH=data/page.db \
	AUTH_TOKEN=devtoken \
	S3_ENDPOINT=localhost:9000 \
	S3_ACCESS_KEY=minioadmin \
	S3_SECRET_KEY=minioadmin

# Run the server against the compose stack.
run:
	$(DEV_ENV) go run ./cmd/server

# Upload a sample Framer-style pack (requires `make up`; server not needed).
seed:
	$(DEV_ENV) go run ./cmd/seed

test:
	go test ./...

# Integration tests boot real MinIO via testcontainers (needs Docker).
test-integration:
	go test -tags=integration ./...

tidy:
	go mod tidy
