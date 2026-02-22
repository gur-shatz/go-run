# github.com/gur-shatz/go-run

.PHONY: build test clean install

build:
	@mkdir -p bin
	go build -o bin/execrun ./cmd/execrun
	go build -o bin/runctl ./cmd/runctl
	go build -o bin/runui  ./cmd/runui


test:
	go run github.com/onsi/ginkgo/v2/ginkgo ./...

clean:
	rm -rf bin
	go clean

install: build
	go install ./cmd/execrun
	go install ./cmd/runctl
	go install ./cmd/runui
