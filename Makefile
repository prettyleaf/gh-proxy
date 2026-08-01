VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test race vet lint tidy run docker up down logs token clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/gh-proxy ./cmd/gh-proxy

test:
	go test ./...

race:
	go test -race -count=1 ./...

vet:
	go vet ./...

lint: vet
	gofmt -l -d .

tidy:
	go mod tidy

# Run locally with a throwaway token; prints the base URL to use.
run: build
	@GHP_TOKEN=$${GHP_TOKEN:-local-dev-token-0123456789}; \
	 PREFIX=$${GHP_PREFIX:-/ivanghproxy/}; \
	 echo "base URL: http://127.0.0.1:8899$$PREFIX$$GHP_TOKEN/"; \
	 GHP_TOKEN=$$GHP_TOKEN GHP_PREFIX=$$PREFIX GHP_LISTEN=127.0.0.1:8899 ./bin/gh-proxy

docker:
	docker build --build-arg VERSION=$(VERSION) -t gh-proxy:$(VERSION) -t gh-proxy:latest .

up:
	docker compose up -d --build

down:
	docker compose down

logs:
	docker compose logs -f

# Generate a token suitable for GHP_TOKEN.
token:
	@openssl rand -hex 24

clean:
	rm -rf bin
