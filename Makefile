.PHONY: test lint vuln local-release release-dry-run

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//')
LDFLAGS := -X github.com/riftwerx/company-research/internal/version.Version=$(VERSION)

test:
	go test -race ./...

lint:
	golangci-lint run ./...

vuln:
	govulncheck ./...

local-release:
	go install -ldflags "$(LDFLAGS)" ./cmd/company-research

# goreleaser version must match the version pinned in .github/workflows/release.yml.
release-dry-run:
	goreleaser release --snapshot --clean
