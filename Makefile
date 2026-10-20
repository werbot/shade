.PHONY: build test lint web web-deps
.DEFAULT_GOAL := build

web-deps:
	cd web && bun install --frozen-lockfile

web: web-deps
	find web/dist -mindepth 1 ! -name .gitkeep -delete
	cd web && bun run build

build: web
	CGO_ENABLED=0 go build -o shade ./cmd/shade

test: web
	go test ./...
	cd web && bun test

lint: web-deps
	go vet ./...
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt: not formatted:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi
	cd web && bun run check
