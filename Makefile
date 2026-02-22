# github.com/gur-shatz/go-run

VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BRANCH   := $(shell git rev-parse --abbrev-ref HEAD 2>/dev/null || echo unknown)
DATE     := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS_PKG := github.com/gur-shatz/go-run/internal/buildinfo
LDFLAGS := -X $(LDFLAGS_PKG).Version=$(VERSION) \
           -X $(LDFLAGS_PKG).Commit=$(COMMIT) \
           -X $(LDFLAGS_PKG).Branch=$(BRANCH) \
           -X $(LDFLAGS_PKG).Date=$(DATE)

.PHONY: build test clean install

build:
	@mkdir -p bin
	go build -ldflags "$(LDFLAGS)" -o bin/execrun ./cmd/execrun
	go build -ldflags "$(LDFLAGS)" -o bin/runctl ./cmd/runctl

test:
	go run github.com/onsi/ginkgo/v2/ginkgo ./...

clean:
	rm -rf bin
	go clean

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/execrun
	go install -ldflags "$(LDFLAGS)" ./cmd/runctl
