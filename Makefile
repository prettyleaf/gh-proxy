VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
# The rest of what the status page's build popover shows. BUILD_NUMBER is a CI
# run number and stays empty locally; the page hides the rows it has no value for.
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null)
BRANCH ?= $(shell git rev-parse --abbrev-ref HEAD 2>/dev/null)
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
BUILD_NUMBER ?=
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) \
	-X main.branch=$(BRANCH) -X main.buildTime=$(BUILD_TIME) \
	-X main.buildNumber=$(BUILD_NUMBER)

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

# Run locally with a throwaway token; prints the base URL and the status page.
run: build
	@GHP_TOKEN=$${GHP_TOKEN:-local-dev-token-0123456789}; \
	 PREFIX=$${GHP_PREFIX:-/ivanghproxy/}; \
	 STATUS=$${GHP_STATUS_PATH:-/ghp-status}; \
	 echo "base URL:    http://127.0.0.1:8899$$PREFIX$$GHP_TOKEN/"; \
	 echo "status page: http://127.0.0.1:8899$$STATUS?token=$$GHP_TOKEN"; \
	 GHP_TOKEN=$$GHP_TOKEN GHP_PREFIX=$$PREFIX GHP_LISTEN=127.0.0.1:8899 \
	 GHP_STATUS_PATH=$$STATUS ./bin/gh-proxy

docker:
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) \
		--build-arg BRANCH=$(BRANCH) --build-arg BUILD_TIME=$(BUILD_TIME) \
		--build-arg BUILD_NUMBER=$(BUILD_NUMBER) \
		-t gh-proxy:$(VERSION) -t gh-proxy:latest .

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
