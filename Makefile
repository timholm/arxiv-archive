BINARY := archive
MODULE := github.com/timholm/arxiv-archive
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -ldflags "-s -w -X main.version=$(VERSION) -X main.buildTime=$(BUILD_TIME)"

.PHONY: build test clean lint run-serve

build:
	CGO_ENABLED=0 go build $(LDFLAGS) -o $(BINARY) .

test:
	go test -v -race -count=1 ./...

test-short:
	go test -v -short -race -count=1 ./...

clean:
	rm -f $(BINARY)
	go clean -cache -testcache

lint:
	go vet ./...
	test -z "$$(gofmt -l .)"

run-serve:
	go run . serve --addr :9090

docker:
	docker build -t $(BINARY):$(VERSION) .

.DEFAULT_GOAL := build
