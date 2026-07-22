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

.PHONY: build test clean install example-runctl example-backoffice-demo example-supervisor example-supervisor-origin example-supervisor-fixture example-supervisor-local example-supervisor-local-external example-supervisor-leak example-supervisor-factory example-supervisor-factory-bundle example-supervisor-factory-publish example-supervisor-factory-limp package-supervisor deploy-supervisor ship-supervisor

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
	chmod -R u+w examples/supervisor-factory/state 2>/dev/null || true
	rm -rf examples/supervisor-factory/state examples/supervisor-factory/fixture examples/supervisor-factory/factory
	rm -rf examples/supervisor-local/fixture examples/supervisor-local/build/hello/versions
	rm -rf examples/supervisor-local/build/logs examples/supervisor-local/build/supervisor.lock
	rm -f examples/supervisor-local/build/hello/hello.bin examples/supervisor-local/build/hello/stable.txt
	rm -f examples/supervisor-local/build/hello/rejects.txt examples/supervisor-local/build/hello/kill.sock
	rm -rf examples/supervisor-local/build-leak
	go clean

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/execrun
	go install -ldflags "$(LDFLAGS)" ./cmd/runctl
	go install -ldflags "$(LDFLAGS)" ./cmd/supervisor

# runctl example stack (examples/runctl.yaml). Ports derive from BASE_PORT
# (default 28000): runui dashboard http://localhost:28099/, statekit health
# console http://localhost:28099/health/. Override via BASE_PORT / DATA_DIR.
example-runctl:
	cd examples && go run -ldflags "$(LDFLAGS)" ../cmd/runctl -ui -c runctl.yaml

DEMO_PORT ?= 18083
BACKOFFICE_ADDR ?= :19090

# Backoffice demo with generated log files. Main service: http://127.0.0.1:$(DEMO_PORT)
# Backoffice log viewer: http://127.0.0.1$(BACKOFFICE_ADDR)/logs/ (admin / admin123)
example-backoffice-demo:
	cd examples/backoffice-demo && DEMO_PORT=$(DEMO_PORT) BACKOFFICE_ADDR=$(BACKOFFICE_ADDR) go run .

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
	LDFLAGS="$(LDFLAGS)" examples/supervisor-local/run-with-external.sh

example-supervisor-local-external:
	LDFLAGS="$(LDFLAGS)" examples/supervisor-local/run-with-external.sh

# Memory-enforcement demo: a Python component that leaks memory until the
# supervisor kills and restarts it (host mode, no cgroups needed). Watch it at
# http://127.0.0.1:9191/ and /backoffice/memory/incidents.
example-supervisor-leak:
	LDFLAGS="$(LDFLAGS)" examples/supervisor-local/run-leak-demo.sh

SUPERVISOR_FACTORY_DIR     := examples/supervisor-factory
SUPERVISOR_FACTORY_VERSION := v1

# Bake the hello component into ./factory/hello — the demo's stand-in for a
# read-only image layer outside the state dir.
example-supervisor-factory-bundle:
	@rm -rf $(SUPERVISOR_FACTORY_DIR)/factory
	@mkdir -p $(SUPERVISOR_FACTORY_DIR)/factory/hello/bin
	cd examples/supervisor/hello && go build -o ../../supervisor-factory/factory/hello/bin/hello .
	cp examples/supervisor/hello/manifest.yml $(SUPERVISOR_FACTORY_DIR)/factory/hello/manifest.yml
	cp examples/supervisor/hello/greeting.txt.tmpl $(SUPERVISOR_FACTORY_DIR)/factory/hello/greeting.txt.tmpl

# Factory-version demo: empty state dir + no origin → the supervisor falls
# through to the baked factory bundle and runs it. /state on
# http://127.0.0.1:9090. Publish v1 (target below) to watch it switch away
# from the factory; wipe ./state and ./fixture to land on the factory again.
example-supervisor-factory: example-supervisor-factory-bundle
	@rm -rf $(SUPERVISOR_FACTORY_DIR)/state $(SUPERVISOR_FACTORY_DIR)/fixture
	cd $(SUPERVISOR_FACTORY_DIR) && go run -ldflags "$(LDFLAGS)" ../../cmd/supervisor -c supervisor.yml -v

# Publish v1 into the factory demo's file:// fixture (run in a second
# terminal). The running supervisor's next poll downloads and switches to it.
example-supervisor-factory-publish:
	@rm -rf $(SUPERVISOR_FACTORY_DIR)/fixture
	@mkdir -p $(SUPERVISOR_FACTORY_DIR)/fixture/hello/versions $(SUPERVISOR_FACTORY_DIR)/fixture/hello/images
	@printf '%s\n' '$(SUPERVISOR_FACTORY_VERSION)' > $(SUPERVISOR_FACTORY_DIR)/fixture/hello/versions/required.txt
	@rm -rf bin/.factory-stage && mkdir -p bin/.factory-stage/bin
	cd examples/supervisor/hello && go build -o ../../../bin/.factory-stage/bin/hello .
	cp examples/supervisor/hello/manifest.yml bin/.factory-stage/manifest.yml
	cp examples/supervisor/hello/greeting.txt.tmpl bin/.factory-stage/greeting.txt.tmpl
	tar -C bin/.factory-stage -czf $(SUPERVISOR_FACTORY_DIR)/fixture/hello/images/$(SUPERVISOR_FACTORY_VERSION)_$(SUPERVISOR_GOOS)_$(SUPERVISOR_GOARCH).tar.gz bin/hello manifest.yml greeting.txt.tmpl
	@rm -rf bin/.factory-stage
	@echo "published $(SUPERVISOR_FACTORY_VERSION); the running supervisor picks it up on its next poll"

# Limp-mode demo: the same factory boot, but the state dir is read-only
# (docker run --read-only stand-in). The supervisor starts anyway, runs the
# factory version with in-memory state, and /metrics reports
# supervisor_state_writable 0. `make clean` restores the dir's permissions.
example-supervisor-factory-limp: example-supervisor-factory-bundle
	@chmod -R u+w $(SUPERVISOR_FACTORY_DIR)/state 2>/dev/null || true
	@rm -rf $(SUPERVISOR_FACTORY_DIR)/state $(SUPERVISOR_FACTORY_DIR)/fixture
	@mkdir -p $(SUPERVISOR_FACTORY_DIR)/state
	@chmod a-w $(SUPERVISOR_FACTORY_DIR)/state
	cd $(SUPERVISOR_FACTORY_DIR) && go run -ldflags "$(LDFLAGS)" ../../cmd/supervisor -c supervisor.yml -v

package-supervisor:
	./deploy/supervisor/package.sh

deploy-supervisor:
	./deploy/supervisor/deploy.sh

ship-supervisor: package-supervisor deploy-supervisor
