# hello

A minimal HTTP server used to demonstrate the supervisor: remote-driven
updates, crash/rollback handling, per-version logs, and now the **portal**.

## Endpoints

| Path       | Description                                  |
| ---------- | -------------------------------------------- |
| `/`        | Greeting page                                |
| `/greet`   | Returns the rendered greeting                |
| `/healthz` | Liveness probe                               |
| `/state`   | statekit state document                      |
| `/metrics` | Prometheus exposition                        |

## Try it

- **Open app** launches the live service through the supervisor proxy at
  `/proxy/hello/`.
- The greeting text is templated from `supervisor.yml`'s `vars.GREETING`,
  rendered into `greeting.txt` at launch.

## Notes

This file is plain Markdown referenced from `supervisor.yml`:

```yaml
components:
  - name: hello
    readme: ./readme/hello.md
```

The supervisor renders it to HTML on the component page. Editing this file
takes effect on the next page load — no restart required.
