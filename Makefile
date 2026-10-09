GO ?= go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || printf devel)
BAKE_MODELS ?= 1
PLATFORM ?= linux/amd64
LDFLAGS = -s -w -X github.com/androiddrew/parakeet-run/internal/cli.version=$(VERSION)

.PHONY: build test test-native lint fmt image-cpu image-cuda image-jetson-orin image-jetson-xavier image-jetson-orin-onnxruntime image-jetson-xavier-onnxruntime licenses
build:
	mkdir -p bin
	CGO_ENABLED=1 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/parakeet-run ./cmd/parakeet-run
test:
	$(GO) vet ./...
	$(GO) test -race ./...
test-native:
	@test -n "$(PARAKEET_RUN_TEST_ASSETS)" -a -n "$(PARAKEET_RUN_TEST_ORT)" -a -n "$(PARAKEET_RUN_TEST_PROVIDER)" || (printf 'Set PARAKEET_RUN_TEST_ASSETS, PARAKEET_RUN_TEST_ORT and PARAKEET_RUN_TEST_PROVIDER\n' >&2; exit 1)
	$(GO) test -race -count=1 -timeout=30m -v ./integration
fmt:
	gofmt -w cmd internal integration scripts
lint:
	@test -z "$(shell gofmt -l cmd internal integration scripts)" || (printf 'Run make fmt\n' >&2; exit 1)
	$(GO) run honnef.co/go/tools/cmd/staticcheck@v0.8.1 ./...
	$(GO) run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...
licenses:
	$(GO) mod download
	$(GO) run scripts/collect-licenses.go tmp/licenses
image-cpu:
	docker build --platform $(PLATFORM) -f docker/Dockerfile.cpu --build-arg VERSION=$(VERSION) --build-arg BAKE_MODELS=$(BAKE_MODELS) -t parakeet-run:cpu .
image-cuda:
	docker build --platform linux/amd64 -f docker/Dockerfile.cuda --build-arg VERSION=$(VERSION) --build-arg BAKE_MODELS=$(BAKE_MODELS) -t parakeet-run:cuda .
image-jetson-orin-onnxruntime:
	docker build -f docker/jetson-orin/Dockerfile.onnxruntime -t onnxruntime-jetson-orin:1.23.0-cuda13.2 docker/jetson-orin
image-jetson-xavier-onnxruntime:
	docker build -f docker/jetson-xavier/Dockerfile.onnxruntime -t onnxruntime-jetson-xavier:1.22.2-cuda12.2 docker/jetson-xavier
image-jetson-orin:
	docker build -f docker/jetson-orin/Dockerfile --build-arg VERSION=$(VERSION) --build-arg BAKE_MODELS=$(BAKE_MODELS) -t parakeet-run:jetson-orin .
image-jetson-xavier:
	docker build -f docker/jetson-xavier/Dockerfile --build-arg VERSION=$(VERSION) --build-arg BAKE_MODELS=$(BAKE_MODELS) -t parakeet-run:jetson-xavier .
