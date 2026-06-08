#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

mkdir -p examples/supervisor-local/build/external
(cd examples/supervisor/hello && GOTOOLCHAIN=auto go build -o ../../supervisor-local/build/external/hello.bin .)

mkdir -p examples/supervisor-local/build/hello/versions/rejected-running examples/supervisor-local/fixture/hello/versions
printf '%s\n' 'rejected-running' > examples/supervisor-local/build/hello/current.txt
printf '%s\n%s\n' 'rejected-running' 'rejected-required' > examples/supervisor-local/build/hello/rejects.txt
printf '%s\n' 'rejected-required' > examples/supervisor-local/fixture/hello/versions/required.txt
cp examples/supervisor-local/build/hello/manifest.yml examples/supervisor-local/build/hello/versions/rejected-running/manifest.yml
cp examples/supervisor-local/build/hello/greeting.txt.tmpl examples/supervisor-local/build/hello/versions/rejected-running/greeting.txt.tmpl
(cd examples/supervisor/hello && GOTOOLCHAIN=auto go build -o ../../supervisor-local/build/hello/versions/rejected-running/hello.bin .)

GREETING="Hello from external supervisor component" \
OP_VERSION="external-local" \
examples/supervisor-local/build/external/hello.bin --port=18092 &
external_pid=$!

cleanup() {
  kill "$external_pid" 2>/dev/null || true
  wait "$external_pid" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

for _ in {1..50}; do
  if curl -fsS http://127.0.0.1:18092/healthz >/dev/null 2>&1; then
    break
  fi
  sleep 0.1
done

cd examples/supervisor-local
go run -ldflags "${LDFLAGS:-}" ../../cmd/supervisor -c supervisor.yml -v
