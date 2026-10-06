.PHONY: build test test-offline image
build:
	mkdir -p bin
	CGO_ENABLED=0 go build -buildvcs=false -mod=readonly -trimpath -o bin/chickadee ./cmd/chickadee
	CGO_ENABLED=0 go build -buildvcs=false -trimpath -o bin/chickadee-guest ./cmd/chickadee-guest

test:
	go test -race -mod=readonly ./...
	python3 -m unittest discover -s scripts -p test_setup_app.py

test-offline:
	go test -race ./internal/protocol ./internal/pool ./internal/host ./internal/config ./cmd/chickadee-guest

image:
	./scripts/build-image.sh
