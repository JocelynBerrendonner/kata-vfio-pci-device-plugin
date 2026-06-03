# kata-vfio-pci-device-plugin — build / image helpers
#
# Common knobs:
#   IMG        image repository (default: ghcr.io/jocelynberrendonner/kata-vfio-pci-device-plugin)
#   TAG        image tag         (default: latest)
#   GOFLAGS    extra `go build` flags
#   GOOS/GOARCH cross-build targets
#
# Typical use:
#   make build                  # ./bin/kata-vfio-pci-device-plugin
#   make image                  # docker build .
#   make image push IMG=ghcr.io/me/plugin TAG=v0.1.0

IMG     ?= ghcr.io/jocelynberrendonner/kata-vfio-pci-device-plugin
TAG     ?= latest
GOOS    ?= linux
GOARCH  ?= amd64
GOFLAGS ?=

BIN := bin/kata-vfio-pci-device-plugin

.PHONY: all build clean test tidy image push

all: build

build:
	mkdir -p bin
	GOOS=$(GOOS) GOARCH=$(GOARCH) CGO_ENABLED=0 \
	    go build $(GOFLAGS) -o $(BIN) ./cmd/kata-vfio-pci-device-plugin

test:
	go test ./...

tidy:
	go mod tidy

clean:
	rm -rf bin

image:
	docker build -t $(IMG):$(TAG) .

push:
	docker push $(IMG):$(TAG)
