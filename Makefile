.PHONY: build test run

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o lite2api ./cmd/lite2api

test:
	go test -race ./...

run:
	go run ./cmd/lite2api -config data/config.json
