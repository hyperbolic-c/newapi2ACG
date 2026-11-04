.PHONY: test test-integration test-e2e test-schema test-leak vet lint leak-scan validate

PYTHON ?= python3

test:
	go test ./...

test-integration:
	go test -tags=integration ./...

test-e2e:
	go test -tags=e2e ./test/e2e/...

test-schema:
	$(PYTHON) test/schema/check.py

validate:
	go run ./cmd/migrator validate --config-dir configs

vet:
	go vet ./...

lint: vet
	golangci-lint run

test-leak:
	go test ./test/leak/...

leak-scan: test-leak
	gitleaks dir . --config .gitleaks.toml --redact
	@test ! -d test/output || gitleaks dir test/output --config .gitleaks.toml --redact
