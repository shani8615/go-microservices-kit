.PHONY: tidy test lint up down logs demo

tidy:
	go mod tidy

test:
	go test -race -cover ./...

lint:
	golangci-lint run ./...

up:
	docker compose up -d --build

down:
	docker compose down

logs:
	docker compose logs -f auth orders notifications gateway

# End-to-end smoke test against the running stack.
demo:
	./scripts/demo.sh
