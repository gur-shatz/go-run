# gorun

File watching run utilities for go and general use.

| Tool      | Package       | Description                                                                               |
| --------- | ------------- | ----------------------------------------------------------------------------------------- |
| `gorun`   | `pkg/gorun`   | Drop-in `go run` replacement with auto-rebuild on file changes, can also have config file |
| `execrun` | `pkg/execrun` | Generic, language-agnostic file-watching command runner (YAML config)                     |
| `runctl`  | `pkg/runctl`  | Multi-target orchestrator — manage multiple gorun/execrun targets with HTTP API           |
| `runui`   | `pkg/runui`   | runctl + embedded web dashboard for monitoring and control                                |

All tools use content-based change detection (SHA-256 hashing) with polling and fsnotify.
All rebuilding and watching is based on glob patterns.

## Install

```bash
# gorun — Go auto-rebuild
go install github.com/gur-shatz/go-run@latest

# execrun — generic file watcher
go install github.com/gur-shatz/go-run/cmd/execrun@latest

# runctl — multi-target orchestrator (API only)
go install github.com/gur-shatz/go-run/cmd/runctl@latest

# runui — multi-target orchestrator with web dashboard
go install github.com/gur-shatz/go-run/cmd/runui@latest
```

Or from source:

```bash
make install
```

---

## gorun

Use directly from the CLI or with a config file:

```bash
# CLI — no config needed
gorun ./cmd/server -port 8080

# Config — run with configuration from gorun.yaml
gorun
```

Both modes build to a temp binary, watch for changes (default: `**/*.go`, `go.mod`, `go.sum`), and auto-rebuild on change.

### Config File

`gorun.yaml` (`gorun init` generates a starter):

```yaml
vars:
  PORT: '{{ env "PORT" | default "8080" }}'

watch:
  - "**/*.go"
  - "go.mod"
  - "go.sum"
  - "!vendor/**"

args: './cmd/server -port {{ .PORT }}'

exec:
  - "go generate ./..."
```

| Field   | Required | Description                                                                  |
| ------- | -------- | ---------------------------------------------------------------------------- |
| `vars`  | no       | Template variables (see [Template Variables](#template-variables))            |
| `watch` | no       | Glob patterns for files to watch (defaults to `**/*.go`, `go.mod`, `go.sum`) |
| `args`  | yes      | Build flags + target + app args, parsed like `go run` arguments              |
| `exec`  | no       | Commands to run before `go build`                                            |

### Usage

```
gorun [-c <file>]                   Load config and run
gorun [-c <file>] init              Generate a default config
gorun [-c <file>] sum               Snapshot file hashes
```

### Flags

| Flag                    | Default      | Description                                 |
| ----------------------- | ------------ | ------------------------------------------- |
| `-c, --config <file>`   | `gorun.yaml` | Config file path                            |
| `--poll <duration>`     | `500ms`      | Poll interval for file changes              |
| `--debounce <duration>` | `300ms`      | Nagle debounce window                       |
| `--stdout <file>`       |              | Redirect child stdout to file (append mode) |
| `--stderr <file>`       |              | Redirect child stderr to file (append mode) |
| `-v, --verbose`         | `false`      | Show config, patterns, and file counts      |

`--stdout`/`--stderr` only redirect the child process output; gorun's own messages still print to the terminal.

### Examples

```bash
gorun                                  # Load gorun.yaml from current dir
gorun -c myapp.yaml                    # Use a specific config file
```

### Behavior

- Build failure: previous process keeps running
- Child exits on its own: gorun keeps watching, rebuilds on next change
- Sum files (`gorun.sum`) are persisted and updated on each rebuild

### Library Usage

```go
import (
    "github.com/gur-shatz/go-run/pkg/gorun"
    "github.com/gur-shatz/go-run/pkg/config"
)

cfg, vars, _ := gorun.LoadConfig("gorun.yaml")
// Or with parent vars passed down:
cfg, vars, _ = gorun.LoadConfig("gorun.yaml", config.WithVars(parentVars))

ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
defer cancel()
gorun.Run(ctx, *cfg, gorun.Options{Verbose: true})
```

#### Subprocess Control

Run gorun as a child process and receive structured events:

```go
cmd := gorun.Command("./cmd/server", "-port", "8080")
cmd.OnEvent = func(event gorun.Event) {
    switch event.Type {
    case gorun.EventStarted:
        log.Printf("started pid=%d build=%dms", event.PID, event.BuildTimeMs)
    case gorun.EventRebuilt:
        log.Printf("rebuilt pid=%d", event.PID)
    case gorun.EventBuildFailed:
        log.Printf("build failed: %s", event.Error)
    }
}
cmd.Start()
defer cmd.Stop()
cmd.Wait()
```

---

## execrun

Generic, language-agnostic file-watching command runner. Works with any language or toolchain — configured via a simple YAML file.

Reuses gorun's internals (watcher, hasher, glob, sumfile) with shell commands instead of `go build`.

### Quick Start

```bash
# Generate a starter config
execrun init

# Edit execrun.yaml for your project, then run
execrun
```

### Usage

```
execrun [flags] [command]
execrun init
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

# Exec commands — the last command is the long-running managed process.
exec:
  - "./bin/app"
```

Commands are executed via `sh -c`, so pipes, redirects, and environment variables all work.

| Field   | Required | Description                                                             |
| ------- | -------- | ----------------------------------------------------------------------- |
| `vars`  | no       | Template variables (see [Template Variables](#template-variables))       |
| `watch` | yes      | Glob patterns for files to watch (gitignore-style, `!` for exclusions)  |
| `build` | no       | Preparation commands that run to completion before starting the process |
| `exec`  | no       | Run commands — the last is the managed process. Empty = build-only      |

At least one of `build` or `exec` must be non-empty.

### Examples

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
exec:
  - "./bin/server"
```

### Restart Flow

```
File change detected
  → Run build steps sequentially (fail → keep old process)
  → Stop old process (SIGTERM → 5s timeout → SIGKILL)
  → Start last exec command as new process
```

If there are no build steps, the old process is stopped and restarted directly.

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

## runctl / runui

Multi-target orchestrator. Manage multiple gorun and execrun targets from a single `runctl.yaml`, with an HTTP API for status and control. **runui** adds an embedded web dashboard on top.

```bash
runctl                # API only
runui                 # API + web dashboard at http://localhost:9100
```

### Flags

| Flag              | Default        | Description      |
| ----------------- | -------------- | ---------------- |
| `-c, --config`    | `runctl.yaml`  | Config file path |
| `-v`              | `false`        | Verbose output   |

### Config File

```yaml
vars:
  BASE_PORT: '{{ env "BASE_PORT" | default "8000" }}'
  API_PORT:  "{{ add .BASE_PORT 80 }}"
  UI_PORT:   "{{ add .BASE_PORT 99 }}"
  DATA_DIR:  '{{ env "DATA_DIR" | default "/tmp/runctl-data" }}'

api:
  port: {{ .UI_PORT }}

logs_dir: "{{ .DATA_DIR }}/logs"

targets:
  api:
    type: gorun
    config: services/api/gorun.yaml
    enabled: true
    links:
      - name: HTTP
        url: "http://localhost:{{ .API_PORT }}"

  frontend:
    config: services/frontend/execrun.yaml
```

| Field              | Required | Description                                              |
| ------------------ | -------- | -------------------------------------------------------- |
| `vars`             | no       | Template variables (see [Template Variables](#template-variables)) |
| `api.port`         | no       | HTTP API port (default: 9100)                            |
| `logs_dir`         | no       | Directory for log files (`<target>.build.log`/`.run.log`)|
| `targets`          | yes      | Map of target name to target config                      |
| `targets.*.type`   | no       | `gorun` or `execrun` (default: `execrun`)                |
| `targets.*.config` | yes      | Path to the target's gorun/execrun YAML config           |
| `targets.*.enabled`| no       | Whether to start on launch (default: `true`)             |
| `targets.*.links`  | no       | Named URLs shown in the dashboard                        |

The `config` path is relative to the `runctl.yaml` directory. The target's working directory is derived from the config path's directory.

Resolved vars from `runctl.yaml` are automatically passed down to child gorun/execrun configs via `config.WithVars()`. Child configs can reference parent vars with template syntax (e.g., `{{ .API_PORT | default "8080" }}`) and add their own `vars:` section.

### Web Dashboard (runui)

The web UI provides two tabs:

- **Build** — target type, last build duration/timestamp, build count, errors, and a rebuild button
- **Run** — target state, PID, uptime, restart count, custom links, and start/stop/restart buttons

Each target has a log viewer with virtual scrolling and a real-time tail mode.

### HTTP API

```
GET  /api/health                    Health check
GET  /api/targets                   List all targets
GET  /api/targets/{name}            Get target status
POST /api/targets/{name}/build      Trigger rebuild + restart
POST /api/targets/{name}/start      Start target
POST /api/targets/{name}/stop       Stop target
POST /api/targets/{name}/restart    Stop + rebuild + restart
POST /api/targets/{name}/enable     Enable + start
POST /api/targets/{name}/disable    Disable + stop
GET  /api/targets/{name}/logs       Get logs (?stage=build|run&offset=N&limit=M)
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

Both gorun and execrun use the same glob pattern syntax ([doublestar](https://github.com/bmatcuk/doublestar)):

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

The sum file (e.g., `gorun.sum`, `execrun.sum`) is a human-readable snapshot of watched files and their SHA-256 hashes:

```
cmd/server/main.go a1b2c3d
go.mod 9abcdef
internal/handler.go e4f5678
```

Sum files are derived from the config filename (`x.yaml` generates `x.sum`), persisted in the working directory, and updated on each rebuild.

## Stdout Protocol

gorun emits structured protocol lines to stdout for consumption by parent services:

```
[gorun:<event>] <json>
```

| Event          | Description                            |
| -------------- | -------------------------------------- |
| `started`      | Initial build and run complete         |
| `changed`      | File changes detected, before rebuild  |
| `rebuilt`      | Successful rebuild and restart         |
| `build_failed` | Build failed, old process kept running |
| `stopping`     | Shutting down                          |

Use `gorun.ScanOutput()` or `gorun.ParseProtocolLine()` to parse these events programmatically.

## Template Variables

All YAML configs (`gorun.yaml`, `execrun.yaml`, `runctl.yaml`) support Go template syntax for variable substitution, powered by `pkg/config`.

### `vars:` Section

Define template variables in a top-level `vars:` section. Variables can reference environment variables, provide defaults, and depend on each other:

```yaml
vars:
  BASE_PORT: '{{ env "BASE_PORT" | default "8000" }}'
  API_PORT:  "{{ add .BASE_PORT 80 }}"
  DB_HOST:   '{{ env "DB_HOST" | default "localhost" }}'

exec:
  - './bin/server --port {{ .API_PORT }} --db {{ .DB_HOST }}'
```

The `vars:` section is removed from the final parsed config — it exists only for template resolution.

### Template Syntax

Two delimiter styles are supported (useful when one conflicts with YAML quoting):

| Syntax | Example |
| ------ | ------- |
| `{{ .VAR }}` | `"http://localhost:{{ .API_PORT }}"` |
| `[[ .VAR ]]` | `port: [[.API_PORT]]` |

### Template Functions

| Function | Description | Example |
| -------- | ----------- | ------- |
| `default` | Fallback value if empty/nil | `{{ .PORT \| default "8080" }}` |
| `env` | Read OS environment variable | `{{ env "HOME" }}` |
| `required` | Error if value is empty/nil | `{{ .DB_URL \| required "DB_URL must be set" }}` |
| `add` | Integer addition | `{{ add .BASE_PORT 80 }}` |
| `int` / `asInt` | Cast to integer | `{{ .PORT \| int }}` |

### Resolution

Variables are resolved iteratively (up to 10 passes) to handle dependency chains. For example, `API_PORT` depends on `BASE_PORT` — the resolver evaluates `BASE_PORT` first, then uses its value to resolve `API_PORT`.

**Priority** (highest wins):
1. OS environment variables
2. Parent vars passed via `config.WithVars()` (runctl → child configs)
3. The config's own `vars:` section

### Variable Propagation (runctl)

When runctl loads child configs, resolved vars from `runctl.yaml` are passed down automatically. Child configs can reference parent vars and define their own:

```yaml
# runctl.yaml
vars:
  BASE_PORT: '{{ env "BASE_PORT" | default "8000" }}'
  HELLO_PORT: "{{ add .BASE_PORT 80 }}"

targets:
  hello:
    type: gorun
    config: hello/gorun.yaml
```

```yaml
# hello/gorun.yaml — HELLO_PORT comes from parent
args: './main.go -port {{ .HELLO_PORT | default "8080" }}'
```

### Passing env vars to child processes

Child processes (build steps, exec commands, compiled binaries) inherit the full environment of the parent process. Resolved vars from `runctl.yaml` are also set via `os.Setenv` (without overriding existing env vars), so child processes see them too.

---

## Design

- **Polling + hashing over fsnotify**: simpler, portable, no file descriptor limits on macOS, catches content-only changes
- **`go build` to temp binary**: clean process lifecycle — can keep the old binary running on build failure
- **Nagle debounce**: batches rapid IDE saves into a single rebuild without adding latency to single-file changes
- **Content-based detection**: only rebuilds when file contents actually change, not on metadata updates
