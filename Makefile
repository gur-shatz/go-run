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

.PHONY: build test clean install example-supervisor example-supervisor-origin example-supervisor-fixture example-supervisor-local package-supervisor deploy-supervisor ship-supervisor

build:
	@mkdir -p bin
	go build -ldflags "$(LDFLAGS)" -o bin/execrun ./cmd/execrun
	go build -ldflags "$(LDFLAGS)" -o bin/runctl ./cmd/runctl
	go build -ldflags "$(LDFLAGS)" -o bin/supervisor ./cmd/supervisor

test:
	go run github.com/onsi/ginkgo/v2/ginkgo ./...

clean:
	rm -rf bin
	rm -rf examples/supervisor/state examples/supervisor/fixture examples/supervisor/origin/keys
	rm -rf examples/supervisor-local/build/logs examples/supervisor-local/build/supervisor.lock
	rm -f examples/supervisor-local/build/hello/hello.bin examples/supervisor-local/build/hello/stable.txt
	rm -f examples/supervisor-local/build/hello/rejects.txt examples/supervisor-local/build/hello/kill.sock
	go clean

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/execrun
	go install -ldflags "$(LDFLAGS)" ./cmd/runctl
	go install -ldflags "$(LDFLAGS)" ./cmd/supervisor

SUPERVISOR_GOOS    := $(shell go env GOOS)
SUPERVISOR_GOARCH  := $(shell go env GOARCH)
SUPERVISOR_FIXTURE := examples/supervisor/fixture
SUPERVISOR_VERSION := v1

# Build the hello component, tarball it, and place required.txt + archive into
# the file:// fixture the example supervisor.yml points at.
example-supervisor-fixture:
	@rm -rf $(SUPERVISOR_FIXTURE)
	@mkdir -p $(SUPERVISOR_FIXTURE)/hello/versions $(SUPERVISOR_FIXTURE)/hello/images
	@printf '%s\n' '$(SUPERVISOR_VERSION)' > $(SUPERVISOR_FIXTURE)/hello/versions/required.txt
	@rm -rf bin/.fixture-stage && mkdir -p bin/.fixture-stage/bin
	cd examples/supervisor/hello && go build -o ../../../bin/.fixture-stage/bin/hello .
	# Vendor-shipped files that ride along inside the tarball: manifest.yml
	# and any templates the supervisor can best-effort validate.
	cp examples/supervisor/hello/manifest.yml bin/.fixture-stage/manifest.yml
	cp examples/supervisor/hello/greeting.txt.tmpl bin/.fixture-stage/greeting.txt.tmpl
	tar -C bin/.fixture-stage -czf $(SUPERVISOR_FIXTURE)/hello/images/$(SUPERVISOR_VERSION)_$(SUPERVISOR_GOOS)_$(SUPERVISOR_GOARCH).tar.gz bin/hello manifest.yml greeting.txt.tmpl
	@rm -rf bin/.fixture-stage
	@echo "fixture ready: $(SUPERVISOR_FIXTURE)/hello/images/$(SUPERVISOR_VERSION)_$(SUPERVISOR_GOOS)_$(SUPERVISOR_GOARCH).tar.gz"

# Standalone: builds the fixture, then runs the supervisor against it via file://.
# No origin process, no signature key. /state on http://127.0.0.1:9090
example-supervisor: example-supervisor-fixture
	cd examples/supervisor && go run -ldflags "$(LDFLAGS)" ../../cmd/supervisor -c supervisor.yml -v

# Alt demo: run the HTTP origin (vendor-side simulator) on :18080. Use a copy
# of supervisor.yml with base_url=http://127.0.0.1:18080 + a real pubkey to
# exercise the signed update flow end-to-end.
example-supervisor-origin:
	cd examples/supervisor/origin && go run . --addr=127.0.0.1:18080 --component=hello --source=../hello --version=v1 --keys=./keys

example-supervisor-local:
	cd examples/supervisor/hello && go build -o ../../supervisor-local/build/hello/hello.bin .
	cd examples/supervisor-local && go run -ldflags "$(LDFLAGS)" ../../cmd/supervisor -c supervisor.yml -v

package-supervisor:
	./deploy/supervisor/package.sh

deploy-supervisor:
	./deploy/supervisor/deploy.sh

ship-supervisor: package-supervisor deploy-supervisor
