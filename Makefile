.PHONY: up down run seed test test-integration tidy

# Infra for local development (MinIO + Postgres).
up:
	docker compose up -d --wait

down:
	docker compose down -v

DEV_ENV = DATABASE_URL=postgres://page:page@localhost:5432/page \
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

# Integration tests boot real MinIO + Postgres via testcontainers (needs Docker).
test-integration:
	go test -tags=integration ./...

migrate:
	$(DEV_ENV) go run ./cmd/seed # migrations run on boot; seed re-runs them

tidy:
	go mod tidy
