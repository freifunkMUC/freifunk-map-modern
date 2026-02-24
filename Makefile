.PHONY: build run clean dev

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")

build:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o freifunk-map .

run: build
	./freifunk-map

dev:
	go run .

clean:
	rm -f freifunk-map

docker:
	docker build -t freifunk-map:$(VERSION) .

# Cross compile for common targets
release:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-s -w" -o freifunk-map-linux-amd64 .
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-s -w" -o freifunk-map-linux-arm64 .
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-s -w" -o freifunk-map-darwin-arm64 .
