.PHONY: run test build tidy

run:
	go run ./cmd/transit-hub

test:
	go test ./...

build:
	go build ./cmd/transit-hub

tidy:
	go mod tidy
