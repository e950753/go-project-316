BIN := bin/hexlet-go-crawler
CMD := ./cmd/hexlet-go-crawler

.PHONY: build test lint run test-coverage

build:
	go build -o $(BIN) $(CMD)

test:
	go test -race ./...

lint:
	golangci-lint run

test-coverage:
	go test -race -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

# Пример: make run URL=https://example.com ARGS="--depth 2"
run:
	go run $(CMD) $(ARGS) $(URL)
