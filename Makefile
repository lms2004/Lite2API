GO ?= go
GO_ENV := GOTOOLCHAIN=local

.PHONY: build test test-docker toolchain run capture-admin

toolchain:
	@$(GO_ENV) $(GO) version
	@$(GO_ENV) $(GO) env GOVERSION

build:
	$(GO_ENV) CGO_ENABLED=0 $(GO) build -trimpath -ldflags="-s -w" -o lite2api ./cmd/lite2api

test:
	$(GO_ENV) $(GO) test -race ./...

test-docker:
	docker run --rm -v "$(CURDIR):/src" -w /src golang:1.26.5-alpine sh -lc 'GOTOOLCHAIN=local go test ./...'

run:
	$(GO_ENV) $(GO) run ./cmd/lite2api -config data/config.json

capture-admin:
	./tools/capture-admin-screenshots.sh
