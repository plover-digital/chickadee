.PHONY: build test test-offline image image-ubuntu-2604 image-minimal-ubuntu-2404
build:
	mkdir -p bin
	CGO_ENABLED=0 go build -buildvcs=false -mod=readonly -trimpath -o bin/chickadee ./cmd/chickadee
	CGO_ENABLED=0 go build -buildvcs=false -trimpath -o bin/chickadee-guest ./cmd/chickadee-guest

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
