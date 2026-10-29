GO ?= go
GO_ENV := GOTOOLCHAIN=local
CONFIG ?= config.example.json

.PHONY: build test test-web test-deploy test-lifecycle verify test-docker toolchain run capture-admin

toolchain:
	@$(GO_ENV) $(GO) version
	@$(GO_ENV) $(GO) env GOVERSION

build:
	$(GO_ENV) CGO_ENABLED=0 $(GO) build -trimpath -ldflags="-s -w" -o lite2api ./cmd/lite2api

test:
	$(GO_ENV) $(GO) test -race ./...

test-web:
	node --test internal/web/*.test.js deploy/migrate-sub2api-auths.test.mjs
	@for file in internal/web/*.js; do node --check "$$file" || exit; done

test-deploy:
	bash deploy/bootstrap.test.sh
	bash deploy/backup-verifiers.test.sh
	bash -n deploy/*.sh deploy/server-ops/*.sh
	PYTHONDONTWRITEBYTECODE=1 python3 deploy/lifecycle_review_test.py
	PYTHONDONTWRITEBYTECODE=1 python3 tools/verify_business_runtime_test.py
	PYTHONDONTWRITEBYTECODE=1 python3 deploy/verify_service_null_test.py
	PYTHONDONTWRITEBYTECODE=1 python3 deploy/verify_service_failure_test.py

# Real age/age-keygen and root/chroot are required. Docker execution is explicit
# with LITE2API_TEST_DOCKER=1; fixtures never use the production Compose project.
test-lifecycle:
	PYTHONDONTWRITEBYTECODE=1 python3 deploy/restore_lifecycle_test.py
	PYTHONDONTWRITEBYTECODE=1 python3 deploy/upgrade_lifecycle_test.py
	PYTHONDONTWRITEBYTECODE=1 python3 deploy/compose_snapshot_test.py

verify: test test-web test-deploy test-lifecycle
	git diff --check

test-docker:
	docker run --rm -v "$(CURDIR):/src" -w /src golang:1.26.5-alpine sh -lc 'GOTOOLCHAIN=local go test ./...'

run:
	@test -f "$(CONFIG)" || { echo "missing CONFIG=$(CONFIG); set CONFIG=/path/to/config.json" >&2; exit 1; }
	$(GO_ENV) $(GO) run ./cmd/lite2api -config "$(CONFIG)"

capture-admin:
	./tools/capture-admin-screenshots.sh
