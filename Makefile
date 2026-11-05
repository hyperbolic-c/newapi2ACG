.PHONY: test test-integration vet lint leak-scan validate

test:
	go test -race ./...

# Needs MIGRATOR_TEST_PG_DSN and MIGRATOR_TEST_MYSQL_DSN (see tests/integration).
test-integration:
	go test -tags=integration -count=1 ./tests/integration/...

vet:
	go vet ./... && go vet -tags=integration ./...

lint: vet
	golangci-lint run ./... && golangci-lint run --build-tags=integration ./...

leak-scan:
	gitleaks dir . --config .gitleaks.toml --redact

validate:
	go run ./cmd/migrator validate --config configs/jobs/newapi-to-acg.yaml
