.PHONY: build test lint

build:
	CGO_ENABLED=0 go build -o shade ./cmd/shade

test:
	go test ./...

lint:
	go vet ./...
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt: not formatted:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi
