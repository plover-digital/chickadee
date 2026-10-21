# A go.mod toolchain suggestion does not exclude vulnerable newer releases.
override export GOTOOLCHAIN := go1.26.10

BIN_DIR ?= bin

.PHONY: build test vulncheck test-offline image image-ubuntu-2604 image-minimal-ubuntu-2404
build:
	mkdir -p "$(BIN_DIR)"
	CGO_ENABLED=0 go build -buildvcs=false -mod=readonly -trimpath -o "$(BIN_DIR)/chickadee" ./cmd/chickadee
	CGO_ENABLED=0 go build -buildvcs=false -trimpath -o "$(BIN_DIR)/chickadee-guest" ./cmd/chickadee-guest
	CGO_ENABLED=0 go build -buildvcs=false -mod=readonly -trimpath -o "$(BIN_DIR)/chickadee-worker" ./cmd/chickadee-worker

test:
	go test -race -mod=readonly ./...
	python3 -m unittest discover -s scripts -p 'test_*.py'

test-offline:
	go test -race ./internal/protocol ./internal/pool ./internal/host ./internal/config ./cmd/chickadee-guest

image: image-ubuntu-2604

image-ubuntu-2604:
	./scripts/build-ubuntu-2604-image.sh images developer

image-minimal-ubuntu-2404:
	./scripts/build-image.sh

vulncheck:
	./scripts/vulncheck.sh --build "$(BIN_DIR)"
