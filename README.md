# gorun

File watching run utilities for go and general use.

| Tool      | Package       | Description                                                                               |
| --------- | ------------- | ----------------------------------------------------------------------------------------- |
| `gorun`   | `pkg/gorun`   | Drop-in `go run` replacement with auto-rebuild on file changes, can also have config file |
| `execrun` | `pkg/execrun` | Generic, language-agnostic file-watching command runner (YAML config)                     |
| `runctl`  | `pkg/runctl`  | Multi-target orchestrator — manage multiple gorun/execrun targets from a single config    |
| `runui`   | `pkg/runui`   | Web-UI based Multi-target orchestrator                                                    |

All tools use content-based change detection (SHA-256 hashing) with polling and fsnotify.
All rebuilding and watching is based on glob patterns.

## Install

```bash
# gorun — Go auto-rebuild
go install github.com/gur-shatz/go-run@latest

# execrun — generic file watcher
go install github.com/gur-shatz/go-run/cmd/execrun@latest

# runctl — multi-target orchestrator
go install github.com/gur-shatz/go-run/cmd/runctl@latest
```

Or from source:

```bash
make install
```

---

## gorun

Drop-in `go run` replacement with auto-rebuild on file changes.

### Quick Start

```bash
gorun ./cmd/server -port 8080
```

This will:

1. Build `./cmd/server` to a temp binary
2. Run it with `-port 8080`
3. Watch for file changes (default: `**/*.go`, `go.mod`, `go.sum`)
4. On change: rebuild, restart, report what changed

### Usage

```
gorun                             Load gorun.yaml and run
gorun -c <file>                   Load file and run
gorun [flags] [--] [go-build-flags] <target> [app-args...]
gorun [flags] # simple run with auto-reload
gorun [-c <file>] init
gorun [-c <file>] sum
```

### Flags

| Flag                    | Default      | Description                                         |
| ----------------------- | ------------ | --------------------------------------------------- |
| `-c, --config <file>`   | `gorun.yaml` | Config file path                                    |
| `--poll <duration>`     | `500ms`      | Poll interval for file changes                      |
| `--debounce <duration>` | `300ms`      | Nagle debounce window                               |
| `--stdout <file>`       |              | Redirect child process stdout to file (append mode) |
| `--stderr <file>`       |              | Redirect child process stderr to file (append mode) |
| `-v, --verbose`         | `false`      | Show config, patterns, and file counts              |

### Commands

| Command                    | Description                         |
| -------------------------- | ----------------------------------- |
| `gorun init`               | Generate a default `gorun.yaml`     |
| `gorun init -c myapp.yaml` | Generate `myapp.yaml`               |
| `gorun sum`                | Snapshot file hashes to `gorun.sum` |

### Config File

`gorun.yaml`:

```yaml
# File patterns to watch (optional, defaults to **/*.go, go.mod, go.sum)
watch:
  - "**/*.go"
  - "go.mod"
  - "go.sum"
  - "!vendor/**"

# Build target and arguments (required) — parsed like "go run" args
args: "./cmd/server -port 8080"

# Pre-build commands (optional) — run before "go build"
exec:
  - "go generate ./..."
```

| Field   | Required | Description                                                                  |
| ------- | -------- | ---------------------------------------------------------------------------- |
| `watch` | no       | Glob patterns for files to watch (defaults to `**/*.go`, `go.mod`, `go.sum`) |
| `args`  | yes      | Build flags + build target + app args, parsed like `go run` arguments        |
| `exec`  | no       | Commands to run before `go build`                                            |

The `args` string is parsed the same way as CLI arguments: flags before the target go to `go build`, the target is the package/file to build, and everything after goes to the built binary.

### Examples

```bash
# Run with CLI args (no config file needed)
gorun ./cmd/server -port 8080

# Pass build flags with --
gorun -- -race ./cmd/server -port 8080

# Use a config file
gorun -c myapp.yaml

# No args — loads gorun.yaml from current directory
gorun
```

### Output Redirection

```bash
# Redirect child stdout to a log file
gorun --stdout /tmp/app.log ./cmd/server

# Redirect both stdout and stderr
gorun --stdout /tmp/out.log --stderr /tmp/err.log ./cmd/server
```

gorun's own messages (build status, change detection, heartbeat) still print to the terminal. Only the child process's output is redirected.

### Behavior

- If a build fails, the previous process keeps running
- If the child process exits on its own, gorun keeps watching for file changes and rebuilds on the next change
- Sum files (`gorun.sum`) are persisted in the working directory and updated on each rebuild

### Library Usage

```go
import "github.com/gur-shatz/go-run/pkg/gorun"

cfg, err := gorun.LoadConfig("gorun.yaml")
if err != nil {
    log.Fatal(err)
}

ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
defer cancel()

err = gorun.Run(ctx, *cfg, gorun.Options{
    Verbose: true,
})
```

#### Subprocess Control

The `gorun` package also provides a subprocess API for running gorun as a child process and receiving structured events:

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
import "github.com/gur-shatz/go-run/pkg/execrun"

cfg, err := execrun.LoadConfig("execrun.yaml")
if err != nil {
    log.Fatal(err)
}

ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
defer cancel()

err = execrun.Run(ctx, *cfg, execrun.Options{
    Verbose: true,
})
```

---

## runctl

Multi-target orchestrator. Manage multiple gorun and execrun targets from a single `runctl.yaml`, with an HTTP API for status and control.

### Quick Start

```yaml
# runctl.yaml
targets:
  api:
    type: gorun
    config: services/api/gorun.yaml

  frontend:
    config: services/frontend/execrun.yaml
```

```bash
runctl
```

The `config` path is relative to the `runctl.yaml` directory. The target's working directory is derived from the config path's directory.

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

## Environment Variables

All YAML configs support `$VAR` and `${VAR}` syntax for environment variable expansion. Variables are expanded before YAML parsing.

### runctl `env:` block

`runctl.yaml` supports a top-level `env:` block that defines environment variables available to all targets:

```yaml
env:
  PORT: "8080"
  DB_HOST: "localhost"

targets:
  api:
    type: gorun
    config: api/gorun.yaml
    links:
      - name: HTTP
        url: "http://localhost:$PORT"
```

**How it works:**

1. First pass: existing OS env vars are expanded, the `env:` block is extracted
2. Each key-value pair is set via `os.Setenv`
3. Second pass: the full config is re-expanded with the new env vars applied
4. Target configs (gorun.yaml, execrun.yaml) also expand env vars when loaded

This means env vars from the `env:` block propagate into target configs, link URLs, and any other string field.

---

## Design

- **Polling + hashing over fsnotify**: simpler, portable, no file descriptor limits on macOS, catches content-only changes
- **`go build` to temp binary**: clean process lifecycle — can keep the old binary running on build failure
- **Nagle debounce**: batches rapid IDE saves into a single rebuild without adding latency to single-file changes
- **Content-based detection**: only rebuilds when file contents actually change, not on metadata updates
