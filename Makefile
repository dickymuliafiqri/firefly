.PHONY: dev build build-fe build-go test test-load build-loadtest loadtest loadtest-suite clean

# Run live reload (backend + frontend autobuild via Air)
dev:
	@if ! command -v air >/dev/null 2>&1; then \
		echo "Air not found in PATH, checking \$$GOPATH/bin/air..."; \
		if [ -f "$$(go env GOPATH)/bin/air" ]; then \
			"$$(go env GOPATH)/bin/air"; \
		else \
			echo "Installing air..."; \
			go install github.com/air-verse/air@latest; \
			"$$(go env GOPATH)/bin/air"; \
		fi \
	else \
		air; \
	fi

# Build frontend production bundle
build-fe:
	@./scripts/build-frontend.sh

# Build Go binary embedding the frontend bundle
build-go:
	@go build -o firefly cmd/firefly/main.go

# Complete production build
build: build-fe build-go

# Run Go unit and integration tests with race detector
test:
	@go test -count=1 -race ./...

# Build standalone high-concurrency load test CLI binary
build-loadtest:
	@mkdir -p bin
	@go build -o bin/loadtest ./cmd/loadtest

# Run quick 100-concurrency load test
loadtest: build-loadtest
	@./bin/loadtest -mock -c 100 -profile low

# Run full 6-scenario benchmark suite (100 & 1,000 requests on low/med/heavy)
loadtest-suite: build-loadtest
	@./bin/loadtest -mock -suite

# Run manual Go test suite
test-load:
	@go test -v -tags manual -run TestLoadSuite ./cmd/loadtest

# Clean build artifacts
clean:
	@rm -rf tmp firefly bin frontend/dist/assets frontend/dist/index.html frontend/dist/vite.svg
