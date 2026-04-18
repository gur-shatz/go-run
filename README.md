# go-run

File watching run utilities for general use.

| Tool      | Package       | Description                                                                                                  |
| --------- | ------------- | ------------------------------------------------------------------------------------------------------------ |
| `execrun` | `pkg/execrun` | Generic, language-agnostic file-watching command runner (YAML config)                                        |
| `runctl`  | `pkg/runctl`  | Multi-target orchestrator — manage multiple execrun targets with HTTP API and optional web dashboard (`-ui`) |

All tools use content-based change detection (SHA-256 hashing) with polling and fsnotify.
All rebuilding and watching is based on glob patterns.

Additionally, provides helper packages so a go application can work even better with these utilities:

| Library      | Package          | Description                                                                      |
| ------------ | ---------------- | -------------------------------------------------------------------------------- |
| `backoffice` | `pkg/backoffice` | Embeddable admin/debug HTTP server with service status, env, info, pprof         |
| `chiutil`    | `pkg/chiutil`    | Self-documenting route folders for chi routers with auto-generated navigation UI |

## Start With The Examples

If you cloned this repo, the fastest way to understand the project is to run the configs under [`examples/`](./examples).

```bash
# Full multi-target demo with the dashboard
go run ./cmd/runctl -c examples/runctl.yaml -ui

# Or run one example directly with execrun
go run ./cmd/execrun -c examples/hello-go/execrun.yaml
```

Open `http://localhost:28099` for the example `runui` dashboard when running `examples/runctl.yaml`.
The example targets cover:

- `examples/hello-go` — small Go HTTP app
- `examples/ticker` — Go ticker process
- `examples/build-css` — build-only target
- `examples/echo-server` — shell-based HTTP server
- `examples/backoffice-demo` — embedded backoffice demo

## Install

```bash
# execrun — generic file watcher
go install github.com/gur-shatz/go-run/cmd/execrun@latest

# runctl — multi-target orchestrator (API + optional web dashboard)
go install github.com/gur-shatz/go-run/cmd/runctl@latest
```

Or from source:

```bash
make install
```

---

## execrun

Generic, language-agnostic file-watching command runner. Works with any language or toolchain — configured via a simple YAML file.

### Quick Start

```bash
# Run one of the repo examples
go run ./cmd/execrun -c examples/hello-go/execrun.yaml

# Or generate a starter config for your own project
execrun init
execrun
```

### Usage

```
execrun [flags] [command]
execrun init
execrun test
execrun sum
```

### Flags

| Flag                    | Default        | Description                                 |
| ----------------------- | -------------- | ------------------------------------------- |
| `-c, --config <path>`   | `execrun.yaml` | Path to config file                         |
| `--poll <duration>`     | `500ms`        | Poll interval for file changes              |
| `--debounce <duration>` | `300ms`        | Debounce window                             |
| `--stdout <file>`       |                | Redirect child stdout to file (append mode) |
| `--stderr <file>`       |                | Redirect child stderr to file (append mode) |
| `-v`                    | `false`        | Verbose output                              |

### Commands

| Command                      | Description                                   |
| ---------------------------- | --------------------------------------------- |
| `execrun init`               | Generate a starter `execrun.yaml`             |
| `execrun -c myapp.yaml init` | Generate `myapp.yaml`                         |
| `execrun test`               | Run configured `test:` steps and exit         |
| `execrun sum`                | Snapshot watched file hashes to `execrun.sum` |

### Config File

`execrun.yaml`:

```yaml
vars:
  LISTEN_ADDR: '0.0.0.0:{{ .PORT | default "8081" }}'

# File patterns to watch (gitignore-style globs)
watch:
  - "**/*.go"
  - "go.mod"
  - "go.sum"

# Build commands — preparation steps that run to completion.
build:
  - "go build -o ./bin/app ."

# Test commands — run after build and before the managed process starts.
test:
  - "go test ./..."

# Exec commands — the last command is the long-running managed process.
exec:
  - "echo address is {{ .LISTEN_ADDR }}"
  - "./bin/app"
```

| Field   | Required | Description                                                                     |
| ------- | -------- | ------------------------------------------------------------------------------- |
| `vars`  | no       | Template variables (see [Template Variables](#template-variables))              |
| `watch` | yes      | Glob patterns for files to watch (gitignore-style, `!` for exclusions)          |
| `build` | no       | Build commands that run to completion before tests or process start             |
| `test`  | no       | Test commands that run after `build` and before the managed process starts      |
| `exec`  | no       | Run commands — the last is the managed process. Empty = build/test-only target  |

At least one of `build`, `test`, or `exec` must be non-empty.

### Examples

**Go:**

```yaml
watch:
  - "**/*.go"
  - "go.mod"
  - "go.sum"
build:
  - "go build -o ./bin/server ./cmd/server"
test:
  - "go test ./..."
exec:
  - "./bin/server"
```

**Python (no build steps):**

```yaml
watch:
  - "**/*.py"
  - "requirements.txt"
exec:
  - "python app.py"
```

**Node.js with TypeScript:**

```yaml
watch:
  - "src/**/*.ts"
  - "package.json"
build:
  - "npm run build"
exec:
  - "node dist/index.js"
```

**Rust:**

```yaml
watch:
  - "src/**/*.rs"
  - "Cargo.toml"
build:
  - "cargo build"
exec:
  - "./target/debug/myapp"
```

**Build-only (no managed process):**

```yaml
watch:
  - "*.css"
build:
  - "mkdir -p dist && cp style.src.css dist/style.css"
  - "echo CSS build complete"
```

**Test-only:**

```yaml
watch:
  - "**/*.go"
test:
  - "go test ./..."
```

**Multi-step with code generation:**

```yaml
watch:
  - "**/*.go"
  - "api/**/*.proto"
  - "!**/*.pb.go"
build:
  - "protoc --go_out=. api/*.proto"
  - "go generate ./..."
  - "go build -o ./bin/server ./cmd/server"
test:
  - "go test ./..."
exec:
  - "./bin/server"
```

### Restart Flow

```
File change detected
  → Run build steps sequentially (fail → keep old process)
  → Run test steps sequentially (fail → keep old process)
  → Stop old process (SIGTERM → 5s timeout → SIGKILL)
  → Start last exec command as new process
```

If there are no build or test steps, the old process is stopped and restarted directly.

If the managed process exits on its own, execrun waits for the next file change to re-run the pipeline.

### Library Usage

```go
import (
    "github.com/gur-shatz/go-run/pkg/execrun"
    "github.com/gur-shatz/go-run/pkg/config"
)

cfg, vars, err := execrun.LoadConfig("execrun.yaml")
// Or with parent vars:
cfg, vars, err = execrun.LoadConfig("execrun.yaml", config.WithVars(parentVars))
```

---

## runctl

Multi-target orchestrator. Manage multiple execrun targets from a single `runctl.yaml`, with an HTTP API for status and control. Use `-ui` to enable the embedded web dashboard.

```bash
runctl                    # Watch all enabled targets (API + watchers)
runctl -ui                # API + web dashboard at http://localhost:9100
runctl -t api             # Watch only the "api" target
runctl build              # Build all enabled targets and exit
runctl test               # Run tests for all enabled targets and exit
runctl -t api build       # Build only "api" and exit
runctl -t api test        # Test only "api" and exit
runctl sum                # Write .sum files for all enabled targets
runctl -t api -t web sum  # Write .sum files for "api" and "web" only
```

### Commands

| Command | Description                                                                 |
| ------- | --------------------------------------------------------------------------- |
| `init`  | Generate a starter `runctl.yaml`                                            |
| `build` | Run build steps for selected targets and exit (no watchers, no HTTP server) |
| `test`  | Run test steps for selected targets and exit (no watchers, no HTTP server)  |
| `sum`   | Snapshot watched file hashes to `.sum` files and exit                       |

### Flags

| Flag           | Default       | Description                                              |
| -------------- | ------------- | -------------------------------------------------------- |
| `-c, --config` | `runctl.yaml` | Config file path                                         |
| `-t <name>`    |               | Target filter (repeatable). Applies to watch, build, test, sum |
| `-ui`          | `false`       | Serve embedded web dashboard                             |
| `-v`           | `false`       | Verbose output                                           |

The `-t` flag can be specified multiple times to select specific targets. Without `-t`, all enabled targets are used. An error is returned if a target name doesn't exist in the config.

### Config File

```yaml
vars:
  BASE_PORT: '{{ env "BASE_PORT" | default "8000" }}'
  API_PORT: "{{ add .BASE_PORT 80 }}"
  UI_PORT: "{{ add .BASE_PORT 99 }}"
  DATA_DIR: '{{ env "DATA_DIR" | default "/tmp/runctl-data" }}'

api:
  port: { { .UI_PORT } }

logs_dir: "{{ .DATA_DIR }}/logs"

targets:
  api:
    config: services/api/execrun.yaml
    vars:
      GREETING: '{{ env "GREETING" | default "Hello!" }}'
    links:
      - name: HTTP
        url: "http://localhost:{{ .API_PORT }}"

  frontend:
    config: services/frontend/execrun.yaml
```

| Field               | Required | Description                                                               |
| ------------------- | -------- | ------------------------------------------------------------------------- |
| `vars`              | no       | Global template variables (see [Template Variables](#template-variables)) |
| `api.port`          | no       | HTTP API port (default: 9100)                                             |
| `logs_dir`          | no       | Directory for log files (`<target>.build.log`/`.test.log`/`.run.log`)     |
| `targets`           | yes      | Map of target name to target config                                       |
| `targets.*.config`  | yes      | Path to the target's execrun YAML config                                  |
| `targets.*.enabled` | no       | Whether to start on launch (default: `true`)                              |
| `targets.*.vars`    | no       | Per-target template variables (override global vars)                      |
| `targets.*.links`   | no       | Named URLs shown in the dashboard                                         |

The `config` path is relative to the `runctl.yaml` directory. The target's working directory is derived from the config path's directory.

Resolved vars from `runctl.yaml` (both global and per-target) are automatically passed down to child execrun configs via `config.WithVars()`. Per-target vars override global vars of the same key. Child configs can reference parent vars with template syntax (e.g., `{{ .API_PORT | default "8080" }}`) and add their own `vars:` section.

### Web Dashboard (`-ui`)

The web UI provides three tabs:

- **Build** — last build duration/timestamp, build count, errors, and a rebuild button
- **Tests** — last test duration/timestamp, test count, errors, and a re-run button
- **Run** — target state, PID, uptime, restart count, custom links, and start/stop/restart buttons

Each target has a log viewer with virtual scrolling and a real-time tail mode.

### HTTP API

```
GET  /api/health                    Health check
GET  /api/targets                   List all targets
GET  /api/targets/{name}            Get target status
POST /api/targets/{name}/build      Trigger rebuild + restart
POST /api/targets/{name}/test       Trigger tests only
POST /api/targets/{name}/start      Start target
POST /api/targets/{name}/stop       Stop target
POST /api/targets/{name}/restart    Stop + rebuild + restart
POST /api/targets/{name}/enable     Enable + start
POST /api/targets/{name}/disable    Disable + stop
GET  /api/targets/{name}/logs       Get logs (?stage=build|test|run&offset=N&limit=M)
```

### Library Usage

```go
import "github.com/gur-shatz/go-run/pkg/runctl"

cfg, err := runctl.LoadConfig("runctl.yaml")
if err != nil {
    log.Fatal(err)
}

ctl, err := runctl.New(*cfg, ".")
if err != nil {
    log.Fatal(err)
}

ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
defer cancel()

ctl.Run(ctx)
```

---

## Watch Patterns

All tools use the same glob pattern syntax ([doublestar](https://github.com/bmatcuk/doublestar)):

| Pattern                  | Matches                                 |
| ------------------------ | --------------------------------------- |
| `**/*.go`                | All `.go` files recursively             |
| `cmd/**/*.go`            | `.go` files under `cmd/`                |
| `*.go`                   | `.go` files in root only                |
| `{src,internal}/**/*.go` | `.go` files under `src/` or `internal/` |
| `**/*.{go,mdx,yaml}`     | Multiple extensions                     |

Patterns starting with `!` are exclusions:

| Pattern       | Effect                           |
| ------------- | -------------------------------- |
| `!**/*.pb.go` | Exclude protobuf generated files |
| `!vendor/**`  | Exclude vendor directory         |

**Excludes always win.** All include patterns are expanded first, then all exclude patterns are removed. You cannot re-include a file that was excluded.

## Sum File

The sum file (e.g., `execrun.sum`) is a human-readable snapshot of watched files and their SHA-256 hashes:

```
cmd/server/main.go a1b2c3d
go.mod 9abcdef
internal/handler.go e4f5678
```

Sum files are derived from the config filename (`x.yaml` generates `x.sum`), persisted in the working directory, and updated on each rebuild.

## Template Variables

All YAML configs (`execrun.yaml`, `runctl.yaml`) support Go template syntax for variable substitution, powered by `pkg/config`.

### `vars:` Section

Define template variables in a top-level `vars:` section. Variables can reference environment variables, provide defaults, and depend on each other:

```yaml
vars:
  BASE_PORT: '{{ env "BASE_PORT" | default "8000" }}'
  API_PORT: "{{ add .BASE_PORT 80 }}"
  DB_HOST: '{{ env "DB_HOST" | default "localhost" }}'

exec:
  - "./bin/server --port {{ .API_PORT }} --db {{ .DB_HOST }}"
```

The `vars:` section is removed from the final parsed config — it exists only for template resolution.

### Template Syntax

Two delimiter styles are supported (useful when one conflicts with YAML quoting):

| Syntax       | Example                              |
| ------------ | ------------------------------------ |
| `{{ .VAR }}` | `"http://localhost:{{ .API_PORT }}"` |
| `[[ .VAR ]]` | `port: [[.API_PORT]]`                |

### Template Functions

| Function        | Description                  | Example                                          |
| --------------- | ---------------------------- | ------------------------------------------------ |
| `default`       | Fallback value if empty/nil  | `{{ .PORT \| default "8080" }}`                  |
| `env`           | Read OS environment variable | `{{ env "HOME" }}`                               |
| `required`      | Error if value is empty/nil  | `{{ .DB_URL \| required "DB_URL must be set" }}` |
| `add`           | Integer addition             | `{{ add .BASE_PORT 80 }}`                        |
| `int` / `asInt` | Cast to integer              | `{{ .PORT \| int }}`                             |

### Resolution

Variables are resolved iteratively (up to 10 passes) to handle dependency chains. For example, `API_PORT` depends on `BASE_PORT` — the resolver evaluates `BASE_PORT` first, then uses its value to resolve `API_PORT`.

**Priority** within a single config (highest wins):

1. Parent vars passed via `config.WithVars()` (runctl → child configs)
2. The config's own `vars:` section

Environment variables are **not** implicitly injected into template data. To read an env var, use `{{ env "VAR" }}` explicitly. This gives you full control — a var can read from the environment, provide a default, or ignore the environment entirely.

### Variable Propagation (runctl)

When runctl loads child configs, resolved vars from `runctl.yaml` are passed down automatically. Child configs can reference parent vars and define their own.

#### Global vars

Defined at the top level of `runctl.yaml`. Available to all targets and to the runctl config itself (e.g., `api.port`, `logs_dir`):

```yaml
# runctl.yaml
vars:
  BASE_PORT: '{{ env "BASE_PORT" | default "8000" }}'
  HELLO_PORT: "{{ add .BASE_PORT 80 }}"

targets:
  hello:
    config: hello/execrun.yaml
```

#### Per-target vars

Defined under `targets.<name>.vars`. These are resolved after global vars and can reference global vars via template syntax. Per-target vars override global vars of the same key:

```yaml
vars:
  GREETING: "Hello" # global default

targets:
  hello:
    config: hello/execrun.yaml
    vars:
      GREETING: "Hello from hello!" # overrides global GREETING for this target
      EXTRA: "{{ .BASE_PORT }}-extra" # can reference global vars

  other:
    config: other/execrun.yaml
    # inherits GREETING: "Hello" from global vars
```

The merged vars (global + target overrides) are passed to the child execrun config as template data.

```yaml
# hello/execrun.yaml — HELLO_PORT comes from parent, GREETING is the target override
build:
  - "go build -o ./bin/hello ./main.go"
exec:
  - './bin/hello -port {{ .HELLO_PORT | default "8080" }} -greeting "{{ .GREETING }}"'
```

### How vars reach child processes

Child processes (build steps, exec commands, compiled binaries) receive vars in two ways:

1. **Template substitution** — vars are injected into the child's execrun config at load time, so `{{ .MY_VAR }}` in command strings is replaced before execution.
2. **Environment inheritance** — resolved vars (global and per-target) are set in the process environment via `os.Setenv`. Child processes inherit the full parent environment, so they can read vars as env vars even without template syntax. Per-target vars override global vars in the environment.

---

## Backoffice

`pkg/backoffice` — an embeddable admin/debug HTTP server for Go services. Provides built-in endpoints for health status, environment inspection, runtime info, and pprof profiling, plus an extensible route folder for custom endpoints.

### Quick Start

```go
import "github.com/gur-shatz/go-run/pkg/backoffice"

bo := backoffice.New()

// Add custom endpoints
bo.Folder().GetDesc("/debug", "App debug info", myDebugHandler)

// Optional: protect TCP with basic auth (UDS stays open)
bo.SetAuth("admin", "secret")

// Start on Unix domain socket (no-op if GORUN_BACKOFFICE_SOCK is unset)
bo.ListenAndServeBackground(ctx)

// Start on TCP (independent of UDS)
bo.ListenAndServeTCPBackground(ctx, ":9090")
```

### Built-in Endpoints

| Endpoint        | Description                                                          |
| --------------- | -------------------------------------------------------------------- |
| `/status`       | Service health status (JSON) — see [Service Status](#service-status) |
| `/env`          | Environment variables (sensitive values masked)                      |
| `/info`         | PID, uptime, Go version, goroutines, memory                          |
| `/debug/pprof/` | Go pprof profiling (opens in new tab)                                |
| `/`             | Auto-generated HTML route index                                      |
| `/index.json`   | Machine-readable route index                                         |

### Listening

UDS and TCP are independent — use either or both:

```go
// UDS — driven by GORUN_BACKOFFICE_SOCK env var. No-op if unset.
bo.ListenAndServe(ctx)            // blocking
bo.ListenAndServeBackground(ctx)  // fire-and-forget

// TCP — explicit address
bo.ListenAndServeTCP(ctx, ":9090")            // blocking
bo.ListenAndServeTCPBackground(ctx, ":9090")  // fire-and-forget
```

Both shut down gracefully when `ctx` is cancelled.

### Authentication

```go
bo.SetAuth(username, password string, scope ...AuthScope)
```

| Scope          | Protects         | Default |
| -------------- | ---------------- | ------- |
| `AuthTCPOnly`  | TCP only         | yes     |
| `AuthUnixOnly` | Unix socket only |         |
| `AuthBoth`     | Both transports  |         |

Uses HTTP Basic Auth with constant-time comparison. The default `AuthTCPOnly` is practical: UDS is already protected by filesystem permissions, while TCP is network-exposed.

### Custom Routes

Use `Folder()` to get the root `chiutil.RouteFolder` and register endpoints:

```go
// Single endpoints
bo.Folder().GetDesc("/metrics", "Prometheus metrics", metricsHandler)
bo.Folder().PostDesc("/cache/flush", "Flush cache", flushHandler)

// Sub-folders group related endpoints
app := bo.Folder().Folder("/app")
app.GetDesc("/config", "App configuration", configHandler)
app.GetDesc("/connections", "Connection pools", connHandler)
```

All registered routes automatically appear in the HTML navigation UI.

### Panic Recovery

The backoffice router includes `chi/middleware.Recoverer`. Handler panics return HTTP 500 without crashing the process.

---

## Service Status

`pkg/backoffice` includes a global, thread-safe service status registry. Services register themselves at startup and report their health; the backoffice `/status` endpoint exposes the aggregated state.

### Levels

| Level               | Value | Meaning                       |
| ------------------- | ----- | ----------------------------- |
| `OK`                | 0     | Healthy                       |
| `RunningWithErrors` | 1     | Operational with minor issues |
| `Degraded`          | 2     | Reduced functionality         |
| `Down`              | 3     | Not operational               |

### Usage

```go
// Register at startup — starts at OK
dbSvc := backoffice.CreateServiceStatus("database", true)   // critical
cacheSvc := backoffice.CreateServiceStatus("cache", false)   // non-critical

// Update as conditions change
dbSvc.SetStatus(backoffice.OK, map[string]string{"version": "15.2"})
cacheSvc.SetStatus(backoffice.Down, map[string]string{"error": "connection refused"})

// Read aggregated status
info := backoffice.GetStatus()
// info.GlobalLevel, info.CausedBy, info.Services
```

### Critical vs Non-Critical

- **Critical** services can push the global level to any severity (including `Degraded` and `Down`).
- **Non-critical** services cap their contribution to the global level at `RunningWithErrors`, regardless of their actual level.

This means a non-critical cache going `Down` results in a global `RunningWithErrors`, not `Down`.

### Global Level

The global level is the worst level across all services (after applying the critical/non-critical cap). `CausedBy` names the service responsible.

### Status Response (`GET /status`)

```json
{
  "global_level": "OK",
  "caused_by": "",
  "services": [
    {
      "name": "database",
      "level": "OK",
      "critical": true,
      "time_in_state": "5m30s",
      "data": { "version": "15.2" },
      "history": [{ "timestamp": "...", "level": "OK" }],
      "uptime_pct": 99.8
    }
  ]
}
```

### Per-Service Fields

| Field           | Description                                       |
| --------------- | ------------------------------------------------- |
| `name`          | Service name                                      |
| `level`         | Current level (`OK`, `RUNNING_WITH_ERRORS`, etc.) |
| `critical`      | Whether the service is critical                   |
| `time_in_state` | Duration since last level change                  |
| `data`          | Arbitrary JSON from the last `SetStatus` call     |
| `history`       | Last 10 level changes with timestamps             |
| `uptime_pct`    | Percentage of time _not_ in `Degraded` or `Down`  |

---

## chiutil

`pkg/chiutil` — self-documenting route folders for [chi](https://github.com/go-chi/chi) routers. Wraps a chi router and automatically generates a browsable HTML navigation UI and a JSON index at each level.

### Route Folders

```go
r := chi.NewRouter()
folder := chiutil.NewRouteFolderOn(r, "/")
folder.ServiceName("My Service")
folder.Title("Admin Panel")

// Register routes — they appear in the auto-generated index
folder.GetDesc("/health", "Health check", healthHandler)
folder.PostDesc("/cache/flush", "Flush all caches", flushHandler)
```

Each folder serves:

- `GET /` — HTML navigation page
- `GET /index.json` — machine-readable index

### Sub-Folders

```go
api := folder.Folder("/api")
api.GetDesc("/users", "List users", usersHandler)
api.GetDesc("/config", "Configuration", configHandler)

admin := folder.Folder("/admin")
admin.PostDesc("/restart", "Restart service", restartHandler)
```

Sub-folders appear as navigable directories in the parent's index.

### Route Registration Methods

All methods register the route on the underlying chi router and add it to the folder index:

| Method       | Description                   |
| ------------ | ----------------------------- |
| `Get`        | GET route                     |
| `GetDesc`    | GET route with description    |
| `Post`       | POST route                    |
| `PostDesc`   | POST route with description   |
| `Put`        | PUT route                     |
| `PutDesc`    | PUT route with description    |
| `Patch`      | PATCH route                   |
| `PatchDesc`  | PATCH route with description  |
| `Delete`     | DELETE route                  |
| `DeleteDesc` | DELETE route with description |
| `Handle`     | Any method                    |
| `HandleDesc` | Any method with description   |

### Links and Mounts

```go
// Mount an existing chi router as a sub-folder
folder.MountDesc("/app", "Application routes", appRouter)

// Link to a path registered on a parent router (no handler mounted)
folder.Link("/debug/pprof", "Go profiling")

// External link — opens in a new browser tab
folder.ExternalLink("/debug/pprof", "Go profiling (pprof)")
```

### Wildcard Folders

For dynamic collections where items are added/removed at runtime:

```go
accounts := folder.WildcardFolder("accounts", "accountId", func(r chi.Router) {
    r.Get("/details", detailsHandler)
    r.Get("/settings", settingsHandler)
}).Title("Accounts")

// Manage instances dynamically
accounts.Add("acct-123", "Acme Corp")
accounts.Add("acct-456", "Globex Inc")
accounts.Remove("acct-123")
```

This creates:

- `/accounts/` — lists dynamic instances
- `/accounts/acct-123/` — lists routes for that instance
- `/accounts/acct-123/details` — your handler

### Static Files Folder

```go
folder.StaticFilesFolder("logs", "/var/log/myapp")
```

Creates a browsable file system view. Files larger than 1 MB show a size warning in the preview but can still be downloaded directly.

### FolderIndex JSON

```json
{
  "serviceName": "My Service",
  "title": "Routes",
  "description": "",
  "path": "/",
  "entries": [
    {
      "name": "health",
      "method": "GET",
      "path": "health",
      "description": "Health check"
    },
    { "name": "api", "method": "GET", "path": "api/", "isFolder": true }
  ]
}
```

---

## Design

- **Polling + hashing over fsnotify**: simpler, portable, no file descriptor limits on macOS, catches content-only changes
- **Nagle debounce**: batches rapid IDE saves into a single rebuild without adding latency to single-file changes
- **Content-based detection**: only rebuilds when file contents actually change, not on metadata updates
