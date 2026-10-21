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

# Local unit checks only: never download a compiler or modules, never build releases.
test-offline: override export GOTOOLCHAIN := local
test-offline: override export GOPROXY := off
test-offline: override export GOSUMDB := off
test-offline: override export GOENV := off
test-offline: override export GOWORK := off
test-offline:
	@version=$$(go env GOVERSION); \
	case "$$version" in \
	  go1.26.*) patch=$${version#go1.26.}; floor=10 ;; \
	  go1.27.*) patch=$${version#go1.27.}; floor=3 ;; \
	  *) echo "test-offline requires patched local Go 1.26.10+ or 1.27.3+; found $$version" >&2; exit 1 ;; \
	esac; \
	case "$$patch" in ''|*[!0-9]*) echo "test-offline requires a stable patched local compiler; found $$version" >&2; exit 1 ;; esac; \
	test "$$patch" -ge "$$floor" || { echo "test-offline refuses unpatched local $$version" >&2; exit 1; }
	go test -race -mod=readonly ./internal/protocol ./internal/pool ./internal/host ./internal/config ./cmd/chickadee-guest

image: image-ubuntu-2604

image-ubuntu-2604:
	./scripts/build-ubuntu-2604-image.sh images developer

image-minimal-ubuntu-2404:
	./scripts/build-image.sh

vulncheck:
	./scripts/vulncheck.sh --build "$(BIN_DIR)"
