.PHONY: build test lint

build:
	CGO_ENABLED=0 go build -o shade ./cmd/shade

test:
	go test ./...

lint:
	go vet ./...
	gofmt -l .
