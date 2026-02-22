# go-run

File watching run utilities for general use.

| Tool      | Package       | Description                                                          |
| --------- | ------------- | -------------------------------------------------------------------- |
| `execrun` | `pkg/execrun` | Generic, language-agnostic file-watching command runner (YAML config) |
| `runctl`  | `pkg/runctl`  | Multi-target orchestrator — manage multiple execrun targets with HTTP API and optional web dashboard (`-ui`) |

All tools use content-based change detection (SHA-256 hashing) with polling and fsnotify.
All rebuilding and watching is based on glob patterns.

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
  - "echo address is {{ .LISTEN_ADDR }}"
  - "./bin/app"
```

Commands are executed via `sh -c`, so pipes, redirects, and environment variables all work.

| Field   | Required | Description                                                             |
| ------- | -------- | ----------------------------------------------------------------------- |
| `vars`  | no       | Template variables (see [Template Variables](#template-variables))      |
| `watch` | yes      | Glob patterns for files to watch (gitignore-style, `!` for exclusions)  |
| `build` | no       | Preparation commands that run to completion before starting the process |
| `exec`  | no       | Run commands — the last is the managed process. Empty = build-only      |

At least one of `build` or `exec` must be non-empty.

### Examples

**Go:**

```yaml
watch:
  - "**/*.go"
  - "go.mod"
  - "go.sum"
build:
  - "go build -o ./bin/server ./cmd/server"
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

## runctl

Multi-target orchestrator. Manage multiple execrun targets from a single `runctl.yaml`, with an HTTP API for status and control. Use `-ui` to enable the embedded web dashboard.

```bash
runctl                    # Watch all enabled targets (API + watchers)
runctl -ui                # API + web dashboard at http://localhost:9100
runctl -t api             # Watch only the "api" target
runctl build              # Build all enabled targets and exit
runctl -t api build       # Build only "api" and exit
runctl sum                # Write .sum files for all enabled targets
runctl -t api -t web sum  # Write .sum files for "api" and "web" only
```

### Commands

| Command | Description |
| ------- | ----------- |
| `init`  | Generate a starter `runctl.yaml` |
| `build` | Run build steps for selected targets and exit (no watchers, no HTTP server) |
| `sum`   | Snapshot watched file hashes to `.sum` files and exit |

### Flags

| Flag           | Default       | Description                                              |
| -------------- | ------------- | -------------------------------------------------------- |
| `-c, --config` | `runctl.yaml` | Config file path                                         |
| `-t <name>`    |               | Target filter (repeatable). Applies to watch, build, sum |
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
  port: {{ .UI_PORT }}

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

| Field               | Required | Description                                                        |
| ------------------- | -------- | ------------------------------------------------------------------ |
| `vars`              | no       | Global template variables (see [Template Variables](#template-variables)) |
| `api.port`          | no       | HTTP API port (default: 9100)                                      |
| `logs_dir`          | no       | Directory for log files (`<target>.build.log`/`.run.log`)          |
| `targets`           | yes      | Map of target name to target config                                |
| `targets.*.config`  | yes      | Path to the target's execrun YAML config                           |
| `targets.*.enabled` | no       | Whether to start on launch (default: `true`)                       |
| `targets.*.vars`    | no       | Per-target template variables (override global vars)               |
| `targets.*.links`   | no       | Named URLs shown in the dashboard                                  |

The `config` path is relative to the `runctl.yaml` directory. The target's working directory is derived from the config path's directory.

Resolved vars from `runctl.yaml` (both global and per-target) are automatically passed down to child execrun configs via `config.WithVars()`. Per-target vars override global vars of the same key. Child configs can reference parent vars with template syntax (e.g., `{{ .API_PORT | default "8080" }}`) and add their own `vars:` section.

### Web Dashboard (`-ui`)

The web UI provides two tabs:

- **Build** — last build duration/timestamp, build count, errors, and a rebuild button
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
  GREETING: "Hello"                    # global default

targets:
  hello:
    config: hello/execrun.yaml
    vars:
      GREETING: "Hello from hello!"    # overrides global GREETING for this target
      EXTRA: "{{ .BASE_PORT }}-extra"  # can reference global vars

  other:
    config: other/execrun.yaml
    # inherits GREETING: "Hello" from global vars
```

The merged vars (global + target overrides) are passed to the child execrun config as template data.

```yaml
# hello/execrun.yaml — HELLO_PORT comes from parent, GREETING is the target override
build:
  - 'go build -o ./bin/hello ./main.go'
exec:
  - './bin/hello -port {{ .HELLO_PORT | default "8080" }} -greeting "{{ .GREETING }}"'
```

### How vars reach child processes

Child processes (build steps, exec commands, compiled binaries) receive vars in two ways:

1. **Template substitution** — vars are injected into the child's execrun config at load time, so `{{ .MY_VAR }}` in command strings is replaced before execution.
2. **Environment inheritance** — resolved vars (global and per-target) are set in the process environment via `os.Setenv`. Child processes inherit the full parent environment, so they can read vars as env vars even without template syntax. Per-target vars override global vars in the environment.

---

## Design

- **Polling + hashing over fsnotify**: simpler, portable, no file descriptor limits on macOS, catches content-only changes
- **Nagle debounce**: batches rapid IDE saves into a single rebuild without adding latency to single-file changes
- **Content-based detection**: only rebuilds when file contents actually change, not on metadata updates
