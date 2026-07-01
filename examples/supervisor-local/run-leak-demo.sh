#!/usr/bin/env bash
# Memory-enforcement demo: launch the supervisor with a single Python component
# that leaks memory until the supervisor's enforcer kills and restarts it.
#
#   ./run-leak-demo.sh
#
# Then watch http://127.0.0.1:9191/ — the leaker's memory state climbs
# pass -> warn -> fail, and after `sustained_for` the supervisor restarts it.
# The kills are recorded at /backoffice/memory/incidents.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT/examples/supervisor-local"

command -v python3 >/dev/null 2>&1 || { echo "python3 is required" >&2; exit 1; }

# Lay out the component's local version dir. current.txt = "." selects the
# local version, whose dir is the component root (build-leak/leaker/), so the
# `command` runs the script copied alongside it.
mkdir -p build-leak/leaker
cp leaker.py build-leak/leaker/leaker.py
printf '%s\n' '.' > build-leak/leaker/current.txt

echo "supervisor: http://127.0.0.1:9191/   (memory: /backoffice/memory, incidents: /backoffice/memory/incidents)"
exec go run ../../cmd/supervisor -c supervisor-leak.yml -v
